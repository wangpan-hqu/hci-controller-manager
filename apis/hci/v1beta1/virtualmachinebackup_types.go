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

package v1beta1

import (
	"github.com/wangpan-hqu/hci-controller-manager/pkg/condition"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type BackupType string

const (
	Backup   BackupType = "backup"
	Snapshot BackupType = "snapshot"
)

const (
	// BackupConditionReady is the "ready" condition type
	BackupConditionReady condition.Cond = "Ready"

	// ConditionProgressing is the "progressing" condition type
	BackupConditionProgressing condition.Cond = "InProgress"
)

// VirtualMachineBackupSpec defines the desired state of VirtualMachineBackup
type VirtualMachineBackupSpec struct {
	Source corev1.TypedLocalObjectReference `json:"source"`

	// +kubebuilder:default:="backup"
	// +kubebuilder:validation:Enum=backup;snapshot
	// +kubebuilder:validation:Optional
	Type BackupType `json:"type,omitempty" default:"backup"`
}

// VirtualMachineBackupStatus defines the observed state of VirtualMachineBackup
type VirtualMachineBackupStatus struct {
	SourceUID *types.UID `json:"sourceUID,omitempty"`

	// +optional
	CreationTime *metav1.Time `json:"creationTime,omitempty"`

	// +optional
	BackupTarget *BackupTarget `json:"backupTarget,omitempty"`

	// +optional
	CSIDriverVolumeSnapshotClassNames map[string]string `json:"csiDriverVolumeSnapshotClassNames,omitempty"`

	// +kubebuilder:validation:Required
	// SourceSpec contains the vm spec source of the backup target
	SourceSpec *VirtualMachineSourceSpec `json:"source,omitempty"`

	// +optional
	VolumeBackups []VolumeBackup `json:"volumeBackups,omitempty"`

	// +optional
	SecretBackups []SecretBackup `json:"secretBackups,omitempty"`

	// +optional
	ReadyToUse *bool `json:"readyToUse,omitempty"`

	// +optional
	Error *Error `json:"error,omitempty"`

	// +optional
	Conditions []Condition `json:"conditions,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:shortName=vmbackup;vmbackups,scope=Namespaced
// +kubebuilder:printcolumn:name="SOURCE_KIND",type=string,JSONPath=`.spec.source.kind`
// +kubebuilder:printcolumn:name="SOURCE_NAME",type=string,JSONPath=`.spec.source.name`
// +kubebuilder:printcolumn:name="TYPE",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="READY_TO_USE",type=boolean,JSONPath=`.status.readyToUse`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="ERROR",type=date,JSONPath=`.status.error.message`

// VirtualMachineBackup is the Schema for the virtualmachinebackups API
type VirtualMachineBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMachineBackupSpec    `json:"spec,omitempty"`
	Status *VirtualMachineBackupStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMachineBackupList contains a list of VirtualMachineBackup
type VirtualMachineBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachineBackup `json:"items"`
}

// BackupTarget is where VM Backup stores
type BackupTarget struct {
	Endpoint     string `json:"endpoint,omitempty"`
	BucketName   string `json:"bucketName,omitempty"`
	BucketRegion string `json:"bucketRegion,omitempty"`
}

// VolumeBackup contains the volume data need to restore a PVC
type VolumeBackup struct {
	// +optional
	Name *string `json:"name,omitempty"`

	// +kubebuilder:validation:Required
	VolumeName string `json:"volumeName"`

	// +kubebuilder:default:="driver.longhorn.io"
	// +kubebuilder:validation:Required
	CSIDriverName string `json:"csiDriverName"`

	// +optional
	CreationTime *metav1.Time `json:"creationTime,omitempty"`

	// +kubebuilder:validation:Required
	PersistentVolumeClaim PersistentVolumeClaimSourceSpec `json:"persistentVolumeClaim"`

	// +optional
	LonghornBackupName *string `json:"longhornBackupName,omitempty"`

	// +optional
	ReadyToUse *bool `json:"readyToUse,omitempty"`

	// +optional
	Error *Error `json:"error,omitempty"`
}

// SecretBackup contains the secret data need to restore a secret referenced by the VM
type SecretBackup struct {
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`

	// +optional
	Data map[string][]byte `json:"data,omitempty"`
}

// Error is the last error encountered during the snapshot/restore
type Error struct {
	// +optional
	Time *metav1.Time `json:"time,omitempty"`

	// +optional
	Message *string `json:"message,omitempty"`
}

type PersistentVolumeClaimSourceSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec corev1.PersistentVolumeClaimSpec `json:"spec,omitempty"`
}

func init() {
	SchemeBuilder.Register(&VirtualMachineBackup{}, &VirtualMachineBackupList{})
}
