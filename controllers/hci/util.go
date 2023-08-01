package hci

import (
	"encoding/json"
	"fmt"
	hciv1beta1 "github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/name"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/settings"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

const (
	// UI stores the mapping between access credential secret and ssh keys in the annotation `hci.wjyl.com/dynamic-ssh-key-names`
	// example: '{"secretname1":["sshkeyname1","sshkeyname2"],"secretname2":["sshkeyname3","sshkeyname4"]}'
	dynamicSSHKeyNamesAnnotation = "hci.wjyl.com/dynamic-ssh-key-names"
)

func IsBackupReady(backup *hciv1beta1.VirtualMachineBackup) bool {
	return backup.Status != nil && backup.Status.ReadyToUse != nil && *backup.Status.ReadyToUse
}

func IsBackupProgressing(backup *hciv1beta1.VirtualMachineBackup) bool {
	return GetVMBackupError(backup) == nil &&
		(backup.Status == nil || backup.Status.ReadyToUse == nil || !*backup.Status.ReadyToUse)
}

func isBackupMissingStatus(backup *hciv1beta1.VirtualMachineBackup) bool {
	return backup.Status == nil || backup.Status.SourceSpec == nil || backup.Status.VolumeBackups == nil
}

func IsBackupTargetSame(vmBackupTarget *hciv1beta1.BackupTarget, target *settings.BackupTarget) bool {
	return vmBackupTarget.Endpoint == target.Endpoint && vmBackupTarget.BucketName == target.BucketName && vmBackupTarget.BucketRegion == target.BucketRegion
}

func isBackupTargetOnAnnotation(backup *hciv1beta1.VirtualMachineBackup) bool {
	return backup.Annotations != nil &&
		(backup.Annotations[backupTargetAnnotation] != "" ||
			backup.Annotations[backupBucketNameAnnotation] != "" ||
			backup.Annotations[backupBucketRegionAnnotation] != "")
}

func isVMRestoreProgressing(vmRestore *hciv1beta1.VirtualMachineRestore) bool {
	return vmRestore.Status == nil || vmRestore.Status.Complete == nil || !*vmRestore.Status.Complete
}

func isVMRestoreMissingVolumes(vmRestore *hciv1beta1.VirtualMachineRestore) bool {
	return len(vmRestore.Status.VolumeRestores) == 0 ||
		(!isNewVMOrHasRetainPolicy(vmRestore) && len(vmRestore.Status.DeletedVolumes) == 0)
}

func isNewVMOrHasRetainPolicy(vmRestore *hciv1beta1.VirtualMachineRestore) bool {
	return vmRestore.Spec.NewVM || vmRestore.Spec.DeletionPolicy == hciv1beta1.VirtualMachineRestoreRetain
}

func GetVMBackupError(vmBackup *hciv1beta1.VirtualMachineBackup) *hciv1beta1.Error {
	if vmBackup.Status != nil && vmBackup.Status.Error != nil {
		return vmBackup.Status.Error
	}
	return nil
}

func newReadyCondition(status corev1.ConditionStatus, reason string, message string) hciv1beta1.Condition {
	return hciv1beta1.Condition{
		Type:               hciv1beta1.BackupConditionReady,
		Status:             status,
		Message:            message,
		Reason:             reason,
		LastTransitionTime: currentTime().Format(time.RFC3339),
	}
}

func newProgressingCondition(status corev1.ConditionStatus, reason string, message string) hciv1beta1.Condition {
	return hciv1beta1.Condition{
		Type:   hciv1beta1.BackupConditionProgressing,
		Status: status,
		// wrangler use Reason to determine whether an object is in error state.
		// ref: https://github.com/rancher/wrangler/blob/6970ad98ba7bd2755312ccfc6540a92bc9a9e316/pkg/summary/summarizers.go#L220-L243
		Reason:             reason,
		Message:            message,
		LastTransitionTime: currentTime().Format(time.RFC3339),
	}
}

func updateBackupCondition(ss *hciv1beta1.VirtualMachineBackup, c hciv1beta1.Condition) {
	ss.Status.Conditions = updateCondition(ss.Status.Conditions, c)
}

func updateCondition(conditions []hciv1beta1.Condition, c hciv1beta1.Condition) []hciv1beta1.Condition {
	found := false
	for i := range conditions {
		if conditions[i].Type == c.Type {
			if conditions[i].Status != c.Status || (conditions[i].Reason != c.Reason) || (conditions[i].Message != c.Message) {
				conditions[i] = c
			}
			found = true
			break
		}
	}

	if !found {
		conditions = append(conditions, c)
	}

	return conditions
}

func translateError(e *snapshotv1.VolumeSnapshotError) *hciv1beta1.Error {
	if e == nil {
		return nil
	}

	return &hciv1beta1.Error{
		Message: e.Message,
		Time:    e.Time,
	}
}

// variable so can be overridden in tests
var currentTime = func() *metav1.Time {
	t := metav1.Now()
	return &t
}

func getRestoreID(vmRestore *hciv1beta1.VirtualMachineRestore) string {
	return fmt.Sprintf("%s-%s", vmRestore.Name, vmRestore.UID)
}

func updateRestoreCondition(r *hciv1beta1.VirtualMachineRestore, c hciv1beta1.Condition) {
	r.Status.Conditions = updateCondition(r.Status.Conditions, c)
}

func getNewVolumes(vm *kubevirtv1.VirtualMachineSpec, vmRestore *hciv1beta1.VirtualMachineRestore) ([]kubevirtv1.Volume, error) {
	var newVolumes = make([]kubevirtv1.Volume, len(vm.Template.Spec.Volumes))
	copy(newVolumes, vm.Template.Spec.Volumes)

	for j, vol := range vm.Template.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil {
			for _, vr := range vmRestore.Status.VolumeRestores {
				if vr.VolumeName != vol.Name {
					continue
				}

				nv := vol.DeepCopy()
				nv.PersistentVolumeClaim.ClaimName = vr.PersistentVolumeClaim.ObjectMeta.Name
				newVolumes[j] = *nv
			}
		}
	}
	return newVolumes, nil
}

func getRestorePVCName(vmRestore *hciv1beta1.VirtualMachineRestore, name string) string {
	s := fmt.Sprintf("restore-%s-%s-%s", vmRestore.Spec.VirtualMachineBackupName, vmRestore.UID, name)
	return s
}

func configVMOwner(vm *kubevirtv1.VirtualMachine) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		{
			APIVersion:         kubevirtv1.SchemeGroupVersion.String(),
			Kind:               kubevirtv1.VirtualMachineGroupVersionKind.Kind,
			Name:               vm.Name,
			UID:                vm.UID,
			Controller:         pointer.BoolPtr(true),
			BlockOwnerDeletion: pointer.BoolPtr(true),
		},
	}
}

func sanitizeVirtualMachineAnnotationsForRestore(restore *hciv1beta1.VirtualMachineRestore, annotations map[string]string) (map[string]string, error) {
	dynamicSSHKeyNames := map[string][]string{}
	newDynamicSSHKeyNames := map[string][]string{}
	for key, value := range annotations {
		if key != dynamicSSHKeyNamesAnnotation {
			continue
		}
		if value == "" {
			continue
		}
		if err := json.Unmarshal([]byte(value), &dynamicSSHKeyNames); err != nil {
			return nil, err
		}
		for secretName, sshKeyNames := range dynamicSSHKeyNames {
			newSecretName := getSecretRefName(restore.Spec.Target.Name, secretName)
			newDynamicSSHKeyNames[newSecretName] = sshKeyNames
		}
		newValue, err := json.Marshal(newDynamicSSHKeyNames)
		if err != nil {
			return nil, err
		}
		annotations[key] = string(newValue)
	}
	return annotations, nil
}

func sanitizeVirtualMachineForRestore(restore *hciv1beta1.VirtualMachineRestore, spec kubevirtv1.VirtualMachineInstanceSpec) kubevirtv1.VirtualMachineInstanceSpec {
	for index, credential := range spec.AccessCredentials {
		if sshPublicKey := credential.SSHPublicKey; sshPublicKey != nil && sshPublicKey.Source.Secret != nil {
			spec.AccessCredentials[index].SSHPublicKey.Source.Secret.SecretName = getSecretRefName(restore.Spec.Target.Name, credential.SSHPublicKey.Source.Secret.SecretName)
		}
		if userPassword := credential.UserPassword; userPassword != nil && userPassword.Source.Secret != nil {
			spec.AccessCredentials[index].UserPassword.Source.Secret.SecretName = getSecretRefName(restore.Spec.Target.Name, credential.UserPassword.Source.Secret.SecretName)
		}
	}
	for index, volume := range spec.Volumes {
		if volume.CloudInitNoCloud != nil && volume.CloudInitNoCloud.UserDataSecretRef != nil {
			spec.Volumes[index].CloudInitNoCloud.UserDataSecretRef.Name = getSecretRefName(restore.Spec.Target.Name, volume.CloudInitNoCloud.UserDataSecretRef.Name)
		}
		if volume.CloudInitNoCloud != nil && volume.CloudInitNoCloud.NetworkDataSecretRef != nil {
			spec.Volumes[index].CloudInitNoCloud.NetworkDataSecretRef.Name = getSecretRefName(restore.Spec.Target.Name, volume.CloudInitNoCloud.NetworkDataSecretRef.Name)
		}
	}
	return spec
}

func getSecretRefName(vmName string, secretName string) string {
	// Use secret Hex to avoid the length of secret name exceeding the K8s limit caused by repeated backup and restore
	return fmt.Sprintf("vm-%s-%s-ref", vmName, name.Hex(secretName, 8))
}

func getVMBackupMetadataFileName(vmBackupNamespace, vmBackupName string) string {
	return fmt.Sprintf("%s-%s.cfg", vmBackupNamespace, vmBackupName)
}
