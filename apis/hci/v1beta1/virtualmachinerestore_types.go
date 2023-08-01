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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DeletionPolicy defines that to do with resources when VirtualMachineRestore is deleted
type DeletionPolicy string

const (
	// VirtualMachineRestoreDelete is the default and causes the
	// VirtualMachineRestore deleted resources like PVC to be deleted
	VirtualMachineRestoreDelete DeletionPolicy = "delete"

	// VirtualMachineRestoreRetain causes the VirtualMachineRestore deleted resources like PVC to be retained
	VirtualMachineRestoreRetain DeletionPolicy = "retain"
)

// VirtualMachineRestoreSpec defines the desired state of VirtualMachineRestore
type VirtualMachineRestoreSpec struct {
	// initially only VirtualMachine type supported
	Target corev1.TypedLocalObjectReference `json:"target"`

	// +kubebuilder:validation:Required
	VirtualMachineBackupName string `json:"virtualMachineBackupName"`

	// +kubebuilder:validation:Required
	VirtualMachineBackupNamespace string `json:"virtualMachineBackupNamespace"`

	// +optional
	NewVM bool `json:"newVM,omitempty"`

	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// VirtualMachineRestoreStatus defines the observed state of VirtualMachineRestore
type VirtualMachineRestoreStatus struct {
	// +optional
	VolumeRestores []VolumeRestore `json:"restores,omitempty"`

	// +optional
	RestoreTime *metav1.Time `json:"restoreTime,omitempty"`

	// +optional
	DeletedVolumes []string `json:"deletedVolumes,omitempty"`

	// +optional
	Complete *bool `json:"complete,omitempty"`

	// +optional
	Conditions []Condition `json:"conditions,omitempty"`

	TargetUID *types.UID `json:"targetUID,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:shortName=vmrestore;vmrestores,scope=Namespaced
// +kubebuilder:printcolumn:name="TARGET_KIND",type=string,JSONPath=`.spec.target.kind`
// +kubebuilder:printcolumn:name="TARGET_NAME",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="COMPLETE",type=boolean,JSONPath=`.status.complete`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="ERROR",type=date,JSONPath=`.status.error.message`

// VirtualMachineRestore is the Schema for the virtualmachinerestores API
type VirtualMachineRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMachineRestoreSpec    `json:"spec,omitempty"`
	Status *VirtualMachineRestoreStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMachineRestoreList contains a list of VirtualMachineRestore
type VirtualMachineRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachineRestore `json:"items"`
}

// VolumeRestore contains the volume data need to restore a PVC
type VolumeRestore struct {
	VolumeName string `json:"volumeName,omitempty"`

	PersistentVolumeClaim PersistentVolumeClaimSourceSpec `json:"persistentVolumeClaimSpec,omitempty"`

	VolumeBackupName string `json:"volumeBackupName,omitempty"`
}

func init() {
	SchemeBuilder.Register(&VirtualMachineRestore{}, &VirtualMachineRestoreList{})
}
