package hci

import (
	"context"
	"fmt"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	lhv1beta1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	hciv1beta1 "github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"strings"
)

func (h *VirtualMachineBackupReconciler) updateConditions(vmBackup *hciv1beta1.VirtualMachineBackup) error {
	var vmBackupCpy = vmBackup.DeepCopy()
	if IsBackupProgressing(vmBackupCpy) {
		updateBackupCondition(vmBackupCpy, newProgressingCondition(corev1.ConditionTrue, "", "Operation in progress"))
		updateBackupCondition(vmBackupCpy, newReadyCondition(corev1.ConditionFalse, "", "Not ready"))
	}

	ready := true
	errorMessage := ""
	for _, vb := range vmBackup.Status.VolumeBackups {
		if vb.ReadyToUse == nil || !*vb.ReadyToUse {
			ready = false
		}

		if vb.Error != nil {
			errorMessage = fmt.Sprintf("VolumeSnapshot %s in error state", *vb.Name)
			break
		}
	}

	if ready && (vmBackupCpy.Status.ReadyToUse == nil || !*vmBackupCpy.Status.ReadyToUse) {
		vmBackupCpy.Status.CreationTime = currentTime()
		vmBackupCpy.Status.Error = nil
		updateBackupCondition(vmBackupCpy, newProgressingCondition(corev1.ConditionFalse, "", "Operation complete"))
		updateBackupCondition(vmBackupCpy, newReadyCondition(corev1.ConditionTrue, "", "Operation complete"))
	}

	// check if the status need to update the error status
	if errorMessage != "" && (vmBackupCpy.Status.Error == nil || vmBackupCpy.Status.Error.Message == nil || *vmBackupCpy.Status.Error.Message != errorMessage) {
		vmBackupCpy.Status.Error = &hciv1beta1.Error{
			Time:    currentTime(),
			Message: pointer.StringPtr(errorMessage),
		}
		updateBackupCondition(vmBackupCpy, newProgressingCondition(corev1.ConditionFalse, "Error", errorMessage))
		updateBackupCondition(vmBackupCpy, newReadyCondition(corev1.ConditionFalse, "", "Not Ready"))
	}

	vmBackupCpy.Status.ReadyToUse = pointer.BoolPtr(ready)

	if !reflect.DeepEqual(vmBackup.Status, vmBackupCpy.Status) {
		if err := h.Update(context.TODO(), vmBackupCpy); err != nil {
			return err
		}
	}
	return nil
}

func getVolumeSnapshotContentName(volumeBackup hciv1beta1.VolumeBackup) string {
	return fmt.Sprintf("%s-vsc", *volumeBackup.Name)
}

func (h *VirtualMachineBackupReconciler) updateVolumeSnapshotChanged(key string, snapshot *snapshotv1.VolumeSnapshot) (*snapshotv1.VolumeSnapshot, error) {
	if snapshot == nil || snapshot.DeletionTimestamp != nil {
		return nil, nil
	}

	controllerRef := metav1.GetControllerOf(snapshot)

	// If it has a ControllerRef, that's all that matters.
	if controllerRef != nil {
		ref := h.resolveVolSnapshotRef(snapshot.Namespace, controllerRef)
		if ref == nil {
			return nil, nil
		}

		h.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}})
	}
	return nil, nil
}

func (h *VirtualMachineBackupReconciler) OnLHBackupChanged(key string, lhBackup *lhv1beta1.Backup) (*lhv1beta1.Backup, error) {
	if lhBackup == nil || lhBackup.DeletionTimestamp != nil || lhBackup.Status.SnapshotName == "" {
		return nil, nil
	}

	snapshotContent := &snapshotv1.VolumeSnapshotContent{}
	err := h.Get(context.TODO(), types.NamespacedName{Name: strings.Replace(lhBackup.Status.SnapshotName, "snapshot", "snapcontent", 1)}, snapshotContent)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		return nil, nil
	}

	snapshot := &snapshotv1.VolumeSnapshot{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: snapshotContent.Spec.VolumeSnapshotRef.Namespace, Name: snapshotContent.Spec.VolumeSnapshotRef.Name}, snapshot)
	if err != nil {
		return nil, err
	}

	controllerRef := metav1.GetControllerOf(snapshot)

	if controllerRef != nil {
		vmBackup := h.resolveVolSnapshotRef(snapshot.Namespace, controllerRef)
		if vmBackup == nil || vmBackup.Status == nil || vmBackup.Status.BackupTarget == nil {
			return nil, nil
		}

		vmBackupCpy := vmBackup.DeepCopy()
		for i, volumeBackup := range vmBackupCpy.Status.VolumeBackups {
			if *volumeBackup.Name == snapshot.Name {
				vmBackupCpy.Status.VolumeBackups[i].LonghornBackupName = pointer.StringPtr(lhBackup.Name)
			}
		}

		if !reflect.DeepEqual(vmBackup.Status, vmBackupCpy.Status) {
			if err := h.Update(context.TODO(), vmBackupCpy); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

// resolveVolSnapshotRef returns the controller referenced by a ControllerRef,
// or nil if the ControllerRef could not be resolved to a matching controller
// of the correct Kind.
func (h *VirtualMachineBackupReconciler) resolveVolSnapshotRef(namespace string, controllerRef *metav1.OwnerReference) *hciv1beta1.VirtualMachineBackup {
	// We can't look up by UID, so look up by Name and then verify UID.
	// Don't even try to look up by Name if it's the wrong Kind.
	if controllerRef.Kind != vmBackupKind.Kind {
		return nil
	}
	backup := &hciv1beta1.VirtualMachineBackup{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: controllerRef.Name}, backup)
	if err != nil {
		return nil
	}
	if backup.UID != controllerRef.UID {
		// The controller we found with this Name is not the same one that the
		// ControllerRef points to.
		return nil
	}
	return backup
}
