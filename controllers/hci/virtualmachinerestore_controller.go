/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hci

import (
	"context"
	"fmt"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	lhv1beta1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	"github.com/sirupsen/logrus"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/name"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/ref"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/settings"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/util"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1beta1 "github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
)

var (
	restoreAnnotationsToDelete = []string{
		"pv.kubernetes.io",
		ref.AnnotationSchemaOwnerKeyName,
	}
)

const (
	restoreControllerName = "hci-vm-restore-controller"

	volumeSnapshotKindName = "VolumeSnapshot"
	vmRestoreKindName      = "VirtualMachineRestore"

	restoreNameAnnotation = "restore.hci.wjyl.com/name"
	lastRestoreAnnotation = "restore.hci.wjyl.com/last-restore-uid"

	vmCreatorLabel = "hci.wjyl.com/creator"
	vmNameLabel    = "hci.wjyl.com/vmName"

	restoreErrorEvent    = "VirtualMachineRestoreError"
	restoreCompleteEvent = "VirtualMachineRestoreComplete"
)

// VirtualMachineRestoreReconciler reconciles a VirtualMachineRestore object
type VirtualMachineRestoreReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Recorder   record.EventRecorder
	RestClient *rest.RESTClient
}

//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachinerestores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachinerestores/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachinerestores/finalizers,verbs=update
//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=subresources.kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=subresources.kubevirt.io,resources=virtualmachines/start,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=subresources.kubevirt.io,resources=virtualmachines/stop,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;delete

func (h *VirtualMachineRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	restore := &v1beta1.VirtualMachineRestore{}
	if err := h.Get(ctx, req.NamespacedName, restore); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if restore.DeletionTimestamp != nil {
		return h.OnRestoreRemove(restore)
	}

	if _, err := h.OnRestoreChange(restore); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// OnRestoreChange handles vm restore object on change and reconcile vm retore status
func (h *VirtualMachineRestoreReconciler) OnRestoreChange(restore *v1beta1.VirtualMachineRestore) (ctrl.Result, error) {
	if restore == nil || restore.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	if !isVMRestoreProgressing(restore) {
		return ctrl.Result{}, nil
	}

	if restore.Status == nil {
		return ctrl.Result{}, h.initStatus(restore)
	}

	backup, err := h.getVMBackup(restore)
	if err != nil {
		return ctrl.Result{}, h.updateStatusError(restore, err, true)
	}

	if isVMRestoreMissingVolumes(restore) {
		if err := h.mountLonghornVolumes(backup); err != nil {
			return ctrl.Result{}, h.updateStatusError(restore, err, true)
		}
		return ctrl.Result{}, h.initVolumesStatus(restore, backup)
	}

	vm, isVolumesReady, err := h.reconcileResources(restore, backup)
	if err != nil {
		return ctrl.Result{}, h.updateStatusError(restore, err, true)
	}

	// set vmRestore owner reference to the target VM
	if len(restore.OwnerReferences) == 0 {
		return ctrl.Result{}, h.updateOwnerRefAndTargetUID(restore, vm)
	}

	return ctrl.Result{}, h.updateStatus(restore, backup, vm, isVolumesReady)
}

// OnResotreRemove remove remote vm restore
func (h *VirtualMachineRestoreReconciler) OnRestoreRemove(restore *v1beta1.VirtualMachineRestore) (ctrl.Result, error) {
	if restore == nil || restore.Status == nil {
		return ctrl.Result{}, nil
	}

	backup, err := h.getVMBackup(restore)
	if err != nil {
		//if !apierrors.IsNotFound(err) {
		//	return ctrl.Result{}, err
		//}
		return ctrl.Result{}, err
	}

	for _, volumeBackup := range backup.Status.VolumeBackups {
		volumeSnapshotContent := &snapshotv1.VolumeSnapshotContent{}
		vscName := h.constructVolumeSnapshotContentName(restore.Namespace, restore.Name, *volumeBackup.Name)
		if err := h.Get(context.TODO(), types.NamespacedName{Name: vscName}, volumeSnapshotContent); err != nil {
			if err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else {
			if err = h.Delete(context.TODO(), volumeSnapshotContent); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

func (h *VirtualMachineRestoreReconciler) initStatus(restore *v1beta1.VirtualMachineRestore) error {
	restoreCpy := restore.DeepCopy()

	restoreCpy.Status = &v1beta1.VirtualMachineRestoreStatus{
		Complete: pointer.BoolPtr(false),
		Conditions: []v1beta1.Condition{
			newProgressingCondition(corev1.ConditionTrue, "", "Initializing VirtualMachineRestore"),
			newReadyCondition(corev1.ConditionFalse, "", "Initializing VirtualMachineRestore"),
		},
	}

	if err := h.Update(context.TODO(), restoreCpy); err != nil {
		return err
	}
	return nil
}

func (h *VirtualMachineRestoreReconciler) initVolumesStatus(vmRestore *v1beta1.VirtualMachineRestore, backup *v1beta1.VirtualMachineBackup) error {
	restoreCpy := vmRestore.DeepCopy()

	if restoreCpy.Status.VolumeRestores == nil {
		volumeRestores, err := getVolumeRestores(restoreCpy, backup)
		if err != nil {
			return err
		}
		restoreCpy.Status.VolumeRestores = volumeRestores
	}

	if !isNewVMOrHasRetainPolicy(vmRestore) && vmRestore.Status.DeletedVolumes == nil {
		var deletedVolumes []string
		for _, vol := range backup.Status.VolumeBackups {
			deletedVolumes = append(deletedVolumes, vol.PersistentVolumeClaim.ObjectMeta.Name)
		}
		restoreCpy.Status.DeletedVolumes = deletedVolumes
	}

	if err := h.Update(context.TODO(), restoreCpy); err != nil {
		return err
	}
	return nil
}

// getVM returns restore target VM
func (h *VirtualMachineRestoreReconciler) getVM(vmRestore *v1beta1.VirtualMachineRestore) (*kubevirtv1.VirtualMachine, error) {
	switch vmRestore.Spec.Target.Kind {
	case kubevirtv1.VirtualMachineGroupVersionKind.Kind:
		vm := &kubevirtv1.VirtualMachine{}
		err := h.Get(context.TODO(), types.NamespacedName{Namespace: vmRestore.Namespace, Name: vmRestore.Spec.Target.Name}, vm)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}

		return vm, nil
	}

	return nil, fmt.Errorf("unknown target %+v", vmRestore.Spec.Target)
}

// getVolumeRestores helps to create an array of new restored volumes
func getVolumeRestores(vmRestore *v1beta1.VirtualMachineRestore, backup *v1beta1.VirtualMachineBackup) ([]v1beta1.VolumeRestore, error) {
	restores := make([]v1beta1.VolumeRestore, 0, len(backup.Status.VolumeBackups))
	for _, vb := range backup.Status.VolumeBackups {
		found := false
		for _, vr := range vmRestore.Status.VolumeRestores {
			if vb.VolumeName == vr.VolumeName {
				restores = append(restores, vr)
				found = true
				break
			}
		}

		if !found {
			if vb.Name == nil {
				return nil, fmt.Errorf("VolumeSnapshotName missing %+v", vb)
			}

			vr := v1beta1.VolumeRestore{
				VolumeName: vb.VolumeName,
				PersistentVolumeClaim: v1beta1.PersistentVolumeClaimSourceSpec{
					ObjectMeta: metav1.ObjectMeta{
						Name:      getRestorePVCName(vmRestore, vb.VolumeName),
						Namespace: vmRestore.Namespace,
					},
					Spec: vb.PersistentVolumeClaim.Spec,
				},
				VolumeBackupName: *vb.Name,
			}
			restores = append(restores, vr)
		}
	}
	return restores, nil
}

func (h *VirtualMachineRestoreReconciler) reconcileResources(
	vmRestore *v1beta1.VirtualMachineRestore,
	backup *v1beta1.VirtualMachineBackup,
) (*kubevirtv1.VirtualMachine, bool, error) {
	// reconcile restoring volumes and create new PVC from CSI volumeSnapshot if not exist
	isVolumesReady, err := h.reconcileVolumeRestores(vmRestore, backup)
	if err != nil {
		return nil, false, err
	}

	// reconcile VM
	vm, err := h.reconcileVM(vmRestore, backup)
	if err != nil {
		return nil, false, err
	}

	//restore referenced secrets
	if err := h.reconcileSecretBackups(vmRestore, backup, vm); err != nil {
		return nil, false, err
	}

	return vm, isVolumesReady, nil
}

func (h *VirtualMachineRestoreReconciler) reconcileVolumeRestores(
	vmRestore *v1beta1.VirtualMachineRestore,
	backup *v1beta1.VirtualMachineBackup,
) (bool, error) {
	isVolumesReady := true
	for i, volumeRestore := range vmRestore.Status.VolumeRestores {
		pvc := &corev1.PersistentVolumeClaim{}
		err := h.Get(context.TODO(), types.NamespacedName{Namespace: vmRestore.Namespace, Name: volumeRestore.PersistentVolumeClaim.ObjectMeta.Name}, pvc)
		if apierrors.IsNotFound(err) {
			volumeBackup := backup.Status.VolumeBackups[i]
			if err = h.createRestoredPVC(vmRestore, volumeBackup, volumeRestore); err != nil {
				return false, err
			}
			isVolumesReady = false
			continue
		}
		if err != nil {
			return false, err
		}

		if pvc.Status.Phase == corev1.ClaimPending {
			isVolumesReady = false
		} else if pvc.Status.Phase != corev1.ClaimBound {
			return false, fmt.Errorf("PVC %s/%s in status %q", pvc.Namespace, pvc.Name, pvc.Status.Phase)
		}
	}
	return isVolumesReady, nil
}

func (h *VirtualMachineRestoreReconciler) reconcileVM(
	vmRestore *v1beta1.VirtualMachineRestore,
	backup *v1beta1.VirtualMachineBackup,
) (*kubevirtv1.VirtualMachine, error) {
	// create new VM if it's not exist
	vm, err := h.getVM(vmRestore)
	if err != nil {
		return nil, err
	} else if vm == nil && err == nil {
		vm, err = h.createNewVM(vmRestore, backup)
		if err != nil {
			return nil, err
		}
	}

	// make sure target VM has correct annotations
	restoreID := getRestoreID(vmRestore)
	if lastRestoreID, ok := vm.Annotations[lastRestoreAnnotation]; ok && lastRestoreID == restoreID {
		return vm, nil
	}

	// VM doesn't have correct annotations like restore to existing VM.
	// We update its volumes to new reconsile volumes
	newVolumes, err := getNewVolumes(&backup.Status.SourceSpec.Spec, vmRestore)
	if err != nil {
		return nil, err
	}

	vmCpy := vm.DeepCopy()
	vmCpy.Spec = backup.Status.SourceSpec.Spec
	vmCpy.Spec.Template.Spec.Volumes = newVolumes
	if vmCpy.Annotations == nil {
		vmCpy.Annotations = make(map[string]string)
	}
	vmCpy.Annotations[lastRestoreAnnotation] = restoreID
	vmCpy.Annotations[restoreNameAnnotation] = vmRestore.Name
	delete(vmCpy.Annotations, util.AnnotationVolumeClaimTemplates)

	if err = h.Update(context.TODO(), vmCpy); err != nil {
		return nil, err
	}

	vm = &kubevirtv1.VirtualMachine{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: vmCpy.Namespace, Name: vmCpy.Name}, vm)

	return vm, err
}

func (h *VirtualMachineRestoreReconciler) reconcileSecretBackups(
	vmRestore *v1beta1.VirtualMachineRestore,
	backup *v1beta1.VirtualMachineBackup,
	vm *kubevirtv1.VirtualMachine,
) error {
	ownerRefs := configVMOwner(vm)
	if !vmRestore.Spec.NewVM {
		for _, secretBackup := range backup.Status.SecretBackups {
			if err := h.createOrUpdateSecret(vmRestore.Namespace, secretBackup.Name, secretBackup.Data, ownerRefs); err != nil {
				return err
			}
		}
		return nil
	}

	// Create new secret for new VM
	for _, secretBackup := range backup.Status.SecretBackups {
		newSecretName := getSecretRefName(vmRestore.Spec.Target.Name, secretBackup.Name)
		if err := h.createOrUpdateSecret(vmRestore.Namespace, newSecretName, secretBackup.Data, ownerRefs); err != nil {
			return err
		}
	}
	return nil
}

func (h *VirtualMachineRestoreReconciler) createOrUpdateSecret(namespace, name string, data map[string][]byte, ownerRefs []metav1.OwnerReference) error {
	secret := &corev1.Secret{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		logrus.Infof("create secret %s/%s", namespace, name)
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       namespace,
				OwnerReferences: ownerRefs,
			},
			Data: data,
		}
		if err := h.Create(context.TODO(), secret); err != nil {
			return err
		}
		return nil
	}

	secretCpy := secret.DeepCopy()
	secretCpy.Data = data

	if !reflect.DeepEqual(secret, secretCpy) {
		logrus.Infof("update secret %s/%s", namespace, name)
		if err := h.Update(context.TODO(), secretCpy); err != nil {
			return err
		}
	}
	return nil
}

func (h *VirtualMachineRestoreReconciler) getVMBackup(vmRestore *v1beta1.VirtualMachineRestore) (*v1beta1.VirtualMachineBackup, error) {
	backup := &v1beta1.VirtualMachineBackup{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: vmRestore.Spec.VirtualMachineBackupNamespace, Name: vmRestore.Spec.VirtualMachineBackupName}, backup)
	if err != nil {
		return nil, err
	}

	if !IsBackupReady(backup) {
		return nil, fmt.Errorf("VMBackup %s is not ready", backup.Name)
	}

	if backup.Status.SourceSpec == nil {
		return nil, fmt.Errorf("empty vm backup source spec of %s", backup.Name)
	}

	return backup, nil
}

// createNewVM helps to create new target VM and set the associated owner reference
func (h *VirtualMachineRestoreReconciler) createNewVM(restore *v1beta1.VirtualMachineRestore, backup *v1beta1.VirtualMachineBackup) (*kubevirtv1.VirtualMachine, error) {
	vmName := restore.Spec.Target.Name
	logrus.Infof("restore target does not exist, creating a new vm %s", vmName)

	restoreID := getRestoreID(restore)
	vmCpy := backup.Status.SourceSpec.DeepCopy()

	newAnnotations, err := sanitizeVirtualMachineAnnotationsForRestore(restore, vmCpy.Spec.Template.ObjectMeta.Annotations)
	if err != nil {
		return nil, err
	}

	defaultRunStrategy := kubevirtv1.RunStrategyRerunOnFailure
	if backup.Status.SourceSpec.Spec.RunStrategy != nil {
		defaultRunStrategy = *backup.Status.SourceSpec.Spec.RunStrategy
	}

	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: restore.Namespace,
			Annotations: map[string]string{
				lastRestoreAnnotation: restoreID,
				restoreNameAnnotation: restore.Name,
			},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &defaultRunStrategy,
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: newAnnotations,
					Labels: map[string]string{
						vmCreatorLabel: "hci",
						vmNameLabel:    vmName,
					},
				},
				Spec: sanitizeVirtualMachineForRestore(restore, vmCpy.Spec.Template.Spec),
			},
		},
	}

	newVolumes, err := getNewVolumes(&vm.Spec, restore)
	if err != nil {
		return nil, err
	}
	vm.Spec.Template.Spec.Volumes = newVolumes

	for i := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		// remove the copied mac address of the new VM
		vm.Spec.Template.Spec.Domain.Devices.Interfaces[i].MacAddress = ""
	}

	err = h.Create(context.TODO(), vm)
	if err != nil {
		return nil, err
	}

	newVM := &kubevirtv1.VirtualMachine{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: vm.Namespace, Name: vm.Name}, newVM)
	return newVM, err
}

func (h *VirtualMachineRestoreReconciler) updateOwnerRefAndTargetUID(vmRestore *v1beta1.VirtualMachineRestore, vm *kubevirtv1.VirtualMachine) error {
	restoreCpy := vmRestore.DeepCopy()
	if restoreCpy.Status.TargetUID == nil {
		restoreCpy.Status.TargetUID = &vm.UID
	}

	// set vmRestore owner reference to the target VM
	restoreCpy.SetOwnerReferences(configVMOwner(vm))

	if err := h.Update(context.TODO(), restoreCpy); err != nil {
		return err
	}

	return nil
}

// createRestoredPVC helps to create new PVC from CSI volumeSnapshot
func (h *VirtualMachineRestoreReconciler) createRestoredPVC(
	vmRestore *v1beta1.VirtualMachineRestore,
	volumeBackup v1beta1.VolumeBackup,
	volumeRestore v1beta1.VolumeRestore,
) error {
	if volumeBackup.Name == nil {
		return fmt.Errorf("missing VolumeSnapshot name")
	}

	dataSourceName := *volumeBackup.Name
	if vmRestore.Namespace != volumeBackup.PersistentVolumeClaim.ObjectMeta.Namespace {
		// create volumesnapshot if namespace is different
		volumeSnapshot, err := h.getOrCreateVolumeSnapshot(vmRestore, volumeBackup)
		if err != nil {
			return err
		}
		dataSourceName = volumeSnapshot.Name
	}

	annotations := map[string]string{}
	for key, value := range volumeBackup.PersistentVolumeClaim.ObjectMeta.Annotations {
		needSkip := false
		for _, prefix := range restoreAnnotationsToDelete {
			if strings.HasPrefix(key, prefix) {
				needSkip = true
				break
			}
		}
		if !needSkip {
			annotations[key] = value
		}
	}
	annotations[restoreNameAnnotation] = vmRestore.Name

	err := h.Create(context.TODO(), &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        volumeRestore.PersistentVolumeClaim.ObjectMeta.Name,
			Namespace:   vmRestore.Namespace,
			Labels:      volumeBackup.PersistentVolumeClaim.ObjectMeta.Labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         v1beta1.GroupVersion.String(),
					Kind:               vmRestoreKindName,
					Name:               vmRestore.Name,
					UID:                vmRestore.UID,
					Controller:         pointer.BoolPtr(true),
					BlockOwnerDeletion: pointer.BoolPtr(true),
				},
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: volumeBackup.PersistentVolumeClaim.Spec.AccessModes,
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: pointer.StringPtr(snapshotv1.SchemeGroupVersion.Group),
				Kind:     volumeSnapshotKindName,
				Name:     dataSourceName,
			},
			Resources:        volumeBackup.PersistentVolumeClaim.Spec.Resources,
			StorageClassName: volumeBackup.PersistentVolumeClaim.Spec.StorageClassName,
			VolumeMode:       volumeBackup.PersistentVolumeClaim.Spec.VolumeMode,
		},
	})
	return err
}

func (h *VirtualMachineRestoreReconciler) getOrCreateVolumeSnapshotContent(
	vmRestore *v1beta1.VirtualMachineRestore,
	volumeBackup v1beta1.VolumeBackup,
) (*snapshotv1.VolumeSnapshotContent, error) {
	volumeSnapshotContentName := h.constructVolumeSnapshotContentName(vmRestore.Namespace, vmRestore.Name, *volumeBackup.Name)
	volumeSnapshotContent := &snapshotv1.VolumeSnapshotContent{}
	if err := h.Get(context.TODO(), types.NamespacedName{Name: volumeSnapshotContentName}, volumeSnapshotContent); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	} else {
		return volumeSnapshotContent, nil
	}

	lhBackup := &lhv1beta1.Backup{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: *volumeBackup.LonghornBackupName}, lhBackup)
	if err != nil {
		return nil, err
	}
	// Ref: https://longhorn.io/docs/1.2.3/snapshots-and-backups/csi-snapshot-support/restore-a-backup-via-csi/#restore-a-backup-that-has-no-associated-volumesnapshot
	snapshotHandle := fmt.Sprintf("bs://%s/%s", volumeBackup.PersistentVolumeClaim.ObjectMeta.Name, lhBackup.Name)

	logrus.Debugf("create VolumeSnapshotContent %s ...", volumeSnapshotContentName)
	err = h.Create(context.TODO(), &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      volumeSnapshotContentName,
			Namespace: vmRestore.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: v1beta1.GroupVersion.String(),
					Kind:       vmRestoreKindName,
					Name:       vmRestore.Name,
					UID:        vmRestore.UID,
				},
			},
		},
		Spec: snapshotv1.VolumeSnapshotContentSpec{
			Driver: "driver.longhorn.io",
			// Use Retain policy to prevent LH Backup from being removed when users delete a VM.
			DeletionPolicy: snapshotv1.VolumeSnapshotContentRetain,
			Source: snapshotv1.VolumeSnapshotContentSource{
				SnapshotHandle: pointer.StringPtr(snapshotHandle),
			},
			VolumeSnapshotClassName: pointer.StringPtr(settings.VolumeSnapshotClass.Get()),
			VolumeSnapshotRef: corev1.ObjectReference{
				Name:      h.constructVolumeSnapshotName(vmRestore.Name, *volumeBackup.Name),
				Namespace: vmRestore.Namespace,
			},
		},
	})

	if err != nil {
		return nil, err
	}

	newBackup := &snapshotv1.VolumeSnapshotContent{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: vmRestore.Namespace, Name: volumeSnapshotContentName}, newBackup)
	return newBackup, err
}

func (h *VirtualMachineRestoreReconciler) getOrCreateVolumeSnapshot(
	vmRestore *v1beta1.VirtualMachineRestore,
	volumeBackup v1beta1.VolumeBackup,
) (*snapshotv1.VolumeSnapshot, error) {
	volumeSnapshotName := h.constructVolumeSnapshotName(vmRestore.Name, *volumeBackup.Name)
	volumeSnapshot := &snapshotv1.VolumeSnapshot{}
	if err := h.Get(context.TODO(), types.NamespacedName{Namespace: vmRestore.Namespace, Name: volumeSnapshotName}, volumeSnapshot); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	} else {
		return volumeSnapshot, nil
	}

	volumeSnapshotContent, err := h.getOrCreateVolumeSnapshotContent(vmRestore, volumeBackup)
	if err != nil {
		return nil, err
	}

	logrus.Debugf("create VolumeSnapshot %s/%s", vmRestore.Namespace, volumeSnapshotName)
	err = h.Create(context.TODO(), &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      volumeSnapshotName,
			Namespace: vmRestore.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         v1beta1.GroupVersion.String(),
					Kind:               vmRestoreKindName,
					Name:               vmRestore.Name,
					UID:                vmRestore.UID,
					BlockOwnerDeletion: pointer.BoolPtr(true),
				},
			},
		},
		Spec: snapshotv1.VolumeSnapshotSpec{
			Source: snapshotv1.VolumeSnapshotSource{
				VolumeSnapshotContentName: pointer.StringPtr(volumeSnapshotContent.Name),
			},
			VolumeSnapshotClassName: pointer.StringPtr(settings.VolumeSnapshotClass.Get()),
		},
	})

	if err != nil {
		return nil, err
	}

	newSnapshot := &snapshotv1.VolumeSnapshot{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: vmRestore.Namespace, Name: volumeSnapshotName}, newSnapshot)
	return newSnapshot, err
}

func (h *VirtualMachineRestoreReconciler) deleteOldPVC(vmRestore *v1beta1.VirtualMachineRestore, vm *kubevirtv1.VirtualMachine) error {
	if isNewVMOrHasRetainPolicy(vmRestore) {
		logrus.Infof("skip deleting old PVC of vm %s/%s", vm.Name, vm.Namespace)
		return nil
	}

	// clean up existing pvc
	for _, volName := range vmRestore.Status.DeletedVolumes {
		vol := &corev1.PersistentVolumeClaim{}
		err := h.Get(context.TODO(), types.NamespacedName{Namespace: vmRestore.Namespace, Name: volName}, vol)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		if vol != nil {
			err = h.Delete(context.TODO(), vol)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (h *VirtualMachineRestoreReconciler) startVM(vm *kubevirtv1.VirtualMachine) error {
	runStrategy, err := vm.RunStrategy()
	if err != nil {
		return err
	}

	logrus.Infof("starting the vm %s, current state running:%v", vm.Name, runStrategy)
	switch runStrategy {
	case kubevirtv1.RunStrategyAlways, kubevirtv1.RunStrategyRerunOnFailure:
		return nil
	case kubevirtv1.RunStrategyManual:
		vmi := &kubevirtv1.VirtualMachineInstance{}
		if err := h.Get(context.TODO(), types.NamespacedName{Namespace: vm.Namespace, Name: vm.Name}, vmi); err == nil {
			if vmi != nil && !vmi.IsFinal() && vmi.Status.Phase != kubevirtv1.Unknown && vmi.Status.Phase != kubevirtv1.VmPhaseUnset {
				// vm is already running
				return nil
			}
		}
		return h.RestClient.Put().Namespace(vm.Namespace).Resource("virtualmachines").SubResource("start").Name(vm.Name).Do(context.Background()).Error()
	case kubevirtv1.RunStrategyHalted:
		return h.RestClient.Put().Namespace(vm.Namespace).Resource("virtualmachines").SubResource("start").Name(vm.Name).Do(context.Background()).Error()
	default:
		// skip
	}
	return nil
}

func (h *VirtualMachineRestoreReconciler) updateStatus(
	vmRestore *v1beta1.VirtualMachineRestore,
	backup *v1beta1.VirtualMachineBackup,
	vm *kubevirtv1.VirtualMachine,
	isVolumesReady bool,
) error {
	restoreCpy := vmRestore.DeepCopy()
	if !isVolumesReady {
		updateRestoreCondition(restoreCpy, newProgressingCondition(corev1.ConditionTrue, "", "Creating new PVCs"))
		updateRestoreCondition(restoreCpy, newReadyCondition(corev1.ConditionFalse, "", "Waiting for new PVCs"))
		if !reflect.DeepEqual(vmRestore, restoreCpy) {
			if err := h.Update(context.TODO(), restoreCpy); err != nil {
				return err
			}
		}
		return nil
	}

	// start VM before checking status
	if err := h.startVM(vm); err != nil {
		return h.updateStatusError(vmRestore, fmt.Errorf("failed to start vm, err:%s", err.Error()), false)
	}

	if !vm.Status.Ready {
		message := "Waiting for target vm to be ready"
		updateRestoreCondition(restoreCpy, newProgressingCondition(corev1.ConditionFalse, "", message))
		updateRestoreCondition(restoreCpy, newReadyCondition(corev1.ConditionFalse, "", message))
		if !reflect.DeepEqual(vmRestore, restoreCpy) {
			if err := h.Update(context.TODO(), restoreCpy); err != nil {
				return err
			}
		}
		return nil
	}

	if err := h.deleteOldPVC(restoreCpy, vm); err != nil {
		return h.updateStatusError(vmRestore, fmt.Errorf("error cleaning up, err:%s", err.Error()), false)
	}

	h.Recorder.Eventf(
		restoreCpy,
		corev1.EventTypeNormal,
		restoreCompleteEvent,
		"Successfully completed VirtualMachineRestore %s",
		restoreCpy.Name,
	)

	restoreCpy.Status.RestoreTime = currentTime()
	restoreCpy.Status.Complete = pointer.BoolPtr(true)
	updateRestoreCondition(restoreCpy, newProgressingCondition(corev1.ConditionFalse, "", "Operation complete"))
	updateRestoreCondition(restoreCpy, newReadyCondition(corev1.ConditionTrue, "", "Operation complete"))
	if err := h.Update(context.TODO(), restoreCpy); err != nil {
		return err
	}
	return nil
}

func (h *VirtualMachineRestoreReconciler) updateStatusError(restore *v1beta1.VirtualMachineRestore, err error, createEvent bool) error {
	restoreCpy := restore.DeepCopy()
	updateRestoreCondition(restoreCpy, newProgressingCondition(corev1.ConditionFalse, "Error", err.Error()))
	updateRestoreCondition(restoreCpy, newReadyCondition(corev1.ConditionFalse, "Error", err.Error()))

	if !reflect.DeepEqual(restore, restoreCpy) {
		if createEvent {
			h.Recorder.Eventf(
				restoreCpy,
				corev1.EventTypeWarning,
				restoreErrorEvent,
				"VirtualMachineRestore encountered error %s",
				err.Error(),
			)
		}

		if err2 := h.Update(context.TODO(), restoreCpy); err2 != nil {
			return err2
		}
	}

	return err
}

func (h *VirtualMachineRestoreReconciler) constructVolumeSnapshotName(restoreName, volumeBackupName string) string {
	return name.SafeConcatName("restore", restoreName, volumeBackupName)
}

func (h *VirtualMachineRestoreReconciler) constructVolumeSnapshotContentName(restoreNamespace, restoreName, volumeBackupName string) string {
	// VolumeSnapshotContent is cluster-scoped resource,
	// so adding restoreNamespace to its name to prevent conflict in different namespace with same restore name and backup
	return name.SafeConcatName("restore", restoreNamespace, restoreName, volumeBackupName)
}

// mountLonghornVolumes helps to mount the volumes to host if it is detached
func (h *VirtualMachineRestoreReconciler) mountLonghornVolumes(backup *v1beta1.VirtualMachineBackup) error {
	// we only need to mount LH Volumes for snapshot type.
	if backup.Spec.Type == v1beta1.Backup {
		return nil
	}

	for _, vb := range backup.Status.VolumeBackups {
		pvcNamespace := vb.PersistentVolumeClaim.ObjectMeta.Namespace
		pvcName := vb.PersistentVolumeClaim.ObjectMeta.Name

		pvc := &corev1.PersistentVolumeClaim{}
		err := h.Get(context.TODO(), types.NamespacedName{Namespace: pvcNamespace, Name: pvcName}, pvc)
		if err != nil {
			return fmt.Errorf("failed to get pvc %s/%s, error: %s", pvcNamespace, pvcName, err.Error())
		}

		volume := &lhv1beta1.Volume{}
		err = h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: pvc.Spec.VolumeName}, volume)
		if err != nil {
			return fmt.Errorf("failed to get volume %s/%s, error: %s", util.LonghornSystemNamespaceName, pvc.Spec.VolumeName, err.Error())
		}

		volCpy := volume.DeepCopy()
		if volume.Status.State == lhv1beta1.VolumeStateDetached || volume.Status.State == lhv1beta1.VolumeStateDetaching {
			volCpy.Spec.NodeID = volume.Status.OwnerID
		}

		if !reflect.DeepEqual(volCpy, volume) {
			logrus.Infof("mount detached volume %s to the node %s", volCpy.Name, volCpy.Spec.NodeID)
			if err = h.Update(context.TODO(), volCpy); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualMachineRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualMachineRestore{}).
		Complete(r)
}
