package hci

import (
	"context"
	"fmt"
	_ "github.com/longhorn/backupstore/nfs"
	_ "github.com/longhorn/backupstore/s3"
	"github.com/sirupsen/logrus"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/indexer"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"
)

type PVCReconciler struct {
	client.Client
	Scheme                     *runtime.Scheme
	VmRestoreReconciler        *VirtualMachineRestoreReconciler
	PersistentVolumeClaimCache *indexer.PersistentVolumeClaimCache
}

//+kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;delete

func (h *PVCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	pvc := &corev1.PersistentVolumeClaim{}
	if err := h.Get(ctx, req.NamespacedName, pvc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if pvc == nil || pvc.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	ower, ok := pvc.Annotations[indexer.PVCByVMIndex]
	if ower != "" {
		h.PersistentVolumeClaimCache.Indexer.Add(pvc)
	}

	restoreName, ok := pvc.Annotations[restoreNameAnnotation]
	if !ok {
		return ctrl.Result{}, nil
	}

	logrus.Debugf("handling PVC updating %s/%s", pvc.Namespace, pvc.Name)

	namespacedName := types.NamespacedName{Namespace: pvc.Namespace, Name: restoreName}
	go func(nn types.NamespacedName) {
		time.Sleep(5 * time.Second)
		h.VmRestoreReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	}(namespacedName)

	return ctrl.Result{}, nil
}

func (h *PVCReconciler) SetOwnerlessPVCReference(pvc *corev1.PersistentVolumeClaim, vm *kubevirtv1.VirtualMachine) error {
	if vm == nil || pvc == nil || pvc.DeletionTimestamp != nil {
		return nil
	}

	pvc = pvc.DeepCopy()
	annotations := pvc.Annotations
	if annotations[indexer.PVCByVMIndex] != "" && annotations[indexer.PVCByVMIndex] != vm.Namespace+"/"+vm.Name {
		return fmt.Errorf("the volume has been used by other vm")
	}
	annotations[indexer.PVCByVMIndex] = vm.Namespace + "/" + vm.Name
	pvc.Annotations = annotations
	err := h.Update(context.Background(), pvc)
	return err
}

func (h *PVCReconciler) UnsetBoundedPVCReference(pvc *corev1.PersistentVolumeClaim, vm *kubevirtv1.VirtualMachine) error {
	if vm == nil || pvc == nil || pvc.DeletionTimestamp != nil {
		return nil
	}
	pvc = pvc.DeepCopy()
	annotations := pvc.Annotations
	delete(annotations, indexer.PVCByVMIndex)
	pvc.Annotations = annotations
	err := h.Update(context.Background(), pvc)
	return err
}

func (h *PVCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.PersistentVolumeClaim{}).
		Complete(h)
}
