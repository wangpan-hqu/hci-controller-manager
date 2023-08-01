package hci

import (
	"context"
	"encoding/json"
	"fmt"
	_ "github.com/longhorn/backupstore/nfs"
	_ "github.com/longhorn/backupstore/s3"
	"github.com/rancher/wrangler/pkg/slice"
	"github.com/sirupsen/logrus"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/indexer"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/ref"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/util"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strings"
	"time"
)

type VMReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	VmRestoreReconciler *VirtualMachineRestoreReconciler

	PVCReconciler
}

//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;delete

func (h *VMReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	vm := &kubevirtv1.VirtualMachine{}
	if err := h.Get(ctx, req.NamespacedName, vm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if vm == nil || vm.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	_, ok := vm.Annotations[util.RemovedPVCsAnnotationKey]
	if ok {
		if err := h.UnsetOwnerOfPVCs(vm); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		if err := h.createPVCsFromAnnotation(vm); err != nil {
			return ctrl.Result{}, err
		}

		if err := h.SetOwnerOfPVCs(vm); err != nil {
			return ctrl.Result{}, err
		}
	}

	restoreName, ok := vm.Annotations[restoreNameAnnotation]
	if !ok {
		return ctrl.Result{}, nil
	}

	logrus.Debugf("handling VM updating %s/%s", vm.Namespace, vm.Name)

	namespacedName := types.NamespacedName{Namespace: vm.Namespace, Name: restoreName}
	go func(nn types.NamespacedName) {
		time.Sleep(5 * time.Second)
		h.VmRestoreReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	}(namespacedName)

	return ctrl.Result{}, nil
}

// createPVCsFromAnnotation creates PVCs defined in the volumeClaimTemplates annotation if they don't exist.
func (h *VMReconciler) createPVCsFromAnnotation(vm *kubevirtv1.VirtualMachine) error {
	volumeClaimTemplates, ok := vm.Annotations[util.AnnotationVolumeClaimTemplates]
	if !ok || volumeClaimTemplates == "" {
		return nil
	}
	var pvcs []*corev1.PersistentVolumeClaim
	if err := json.Unmarshal([]byte(volumeClaimTemplates), &pvcs); err != nil {
		return err
	}
	for _, pvc := range pvcs {
		namespacedName := types.NamespacedName{
			Namespace: vm.Namespace,
			Name:      pvc.ObjectMeta.Name,
		}
		pvc.Namespace = vm.Namespace
		if err := h.Get(context.Background(), client.ObjectKey(namespacedName), pvc); err != nil {
			err = h.Create(context.Background(), pvc)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// SetOwnerOfPVCs records the target VirtualMachine as the owner of the PVCs in annotation.
func (h *VMReconciler) SetOwnerOfPVCs(vm *kubevirtv1.VirtualMachine) error {
	if vm.Spec.Template == nil {
		return nil
	}

	pvcNames := getPVCNames(&vm.Spec.Template.Spec)

	vmReferenceKey := ref.Construct(vm.Namespace, vm.Name)
	// get all the volumes attached to this virtual machine
	//
	attachedPVCs, err := h.PersistentVolumeClaimCache.GetByIndex(indexer.PVCByVMIndex, vmReferenceKey)

	if err != nil {
		return fmt.Errorf("failed to get attached PVCs by VM index: %w", err)
	}
	for _, attachedPVC := range attachedPVCs {
		// check if it's still attached
		if pvcNames.Has(attachedPVC.Name) {
			continue
		}

		toUpdate := attachedPVC.DeepCopy()

		annotations := attachedPVC.Annotations
		delete(annotations, indexer.PVCByVMIndex)
		toUpdate.Annotations = annotations

		if err = h.Update(context.Background(), toUpdate); err != nil {
			logrus.Error(fmt.Errorf("failed to clean schema owners for PVC(%s/%s): %w",
				attachedPVC.Namespace, attachedPVC.Name, err))
		}

	}

	var pvcNamespace = vm.Namespace
	for _, pvcName := range pvcNames.List() {
		namespacedName := types.NamespacedName{
			Namespace: vm.Namespace,
			Name:      pvcName,
		}
		pvc := &corev1.PersistentVolumeClaim{}
		var err = h.Get(context.Background(), namespacedName, pvc)
		if err != nil {
			logrus.Error(fmt.Errorf("failed to get PVC(%s/%s): %w", pvcNamespace, pvcName, err))
			continue

		}
		err = h.SetOwnerlessPVCReference(pvc, vm)
		if err != nil {
			logrus.Error(fmt.Errorf("failed to grant VitrualMachine(%s/%s) as PVC(%s/%s)'s owner: %w",
				vm.Namespace, vm.Name, pvcNamespace, pvcName, err))
		}
	}

	return nil
}

// unsetOwnerOfPVCs erases the target VirtualMachine from the owner of the PVCs in annotation.
func (h *VMReconciler) UnsetOwnerOfPVCs(vm *kubevirtv1.VirtualMachine) error {
	var (
		pvcNamespace = vm.Namespace
		pvcNames     = getPVCNames(&vm.Spec.Template.Spec)
		removedPVCs  = getRemovedPVCs(vm)
	)
	for _, pvcName := range pvcNames.List() {
		namespacedName := types.NamespacedName{
			Namespace: vm.Namespace,
			Name:      pvcName,
		}
		pvc := &corev1.PersistentVolumeClaim{}

		if err := h.Get(context.Background(), client.ObjectKey(namespacedName), pvc); err != nil {
			if apierrors.IsNotFound(err) {
				// ignores not-found error, A VM can reference a non-existent PVC.
				continue
			}
			logrus.Error(fmt.Errorf("failed to get PVC(%s/%s): %w", pvcNamespace, pvcName, err))
			continue
		}
		logrus.Info(pvc.Name + "pvc")
		if err := h.UnsetBoundedPVCReference(pvc, vm); err != nil {
			logrus.Error(fmt.Errorf("failed to revoke VitrualMachine(%s/%s) as PVC(%s/%s)'s owner: %w",
				vm.Namespace, vm.Name, pvcNamespace, pvcName, err))
		}

		if slice.ContainsString(removedPVCs, pvcName) {
			if err := h.Delete(context.Background(), pvc); err != nil {
				logrus.Error(err)
			}
		}
	}

	return nil
}

// getPVCNames returns a name set of the PVCs used by the VMI.
func getPVCNames(vmiSpecPtr *kubevirtv1.VirtualMachineInstanceSpec) sets.String {
	var pvcNames = sets.String{}

	for _, volume := range vmiSpecPtr.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName != "" {
			pvcNames.Insert(volume.PersistentVolumeClaim.ClaimName)
		}
	}

	return pvcNames
}

func getRemovedPVCs(vm *kubevirtv1.VirtualMachine) []string {
	return strings.Split(vm.Annotations[util.RemovedPVCsAnnotationKey], ",")
}

func (h *VMReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubevirtv1.VirtualMachine{}).
		Complete(h)
}
