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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	ImageInitialized condition.Cond = "Initialized"
	ImageImported    condition.Cond = "Imported"
)

const (
	VirtualMachineImageSourceTypeDownload     = "download"
	VirtualMachineImageSourceTypeUpload       = "upload"
	VirtualMachineImageSourceTypeExportVolume = "export-from-volume"
)

// VirtualMachineImageSpec defines the desired state of VirtualMachineImage
type VirtualMachineImageSpec struct {
	// +optional
	Description string `json:"description,omitempty"`

	// +kubebuilder:validation:Required
	DisplayName string `json:"displayName"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=download;upload;export-from-volume
	SourceType string `json:"sourceType"`

	// +optional
	PVCName string `json:"pvcName"`

	// +optional
	PVCNamespace string `json:"pvcNamespace"`

	// +optional
	URL string `json:"url"`

	// +optional
	Checksum string `json:"checksum"`

	// +optional
	StorageClassParameters map[string]string `json:"storageClassParameters"`
}

// VirtualMachineImageStatus defines the observed state of VirtualMachineImage
type VirtualMachineImageStatus struct {
	// +optional
	AppliedURL string `json:"appliedUrl,omitempty"`

	// +optional
	Progress int `json:"progress,omitempty"`

	// +optional
	Size int64 `json:"size,omitempty"`

	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// +optional
	Conditions []Condition `json:"conditions,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:shortName=vmimage;vmimages,scope=Namespaced
// +kubebuilder:printcolumn:name="DISPLAY-NAME",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="SIZE",type=integer,JSONPath=`.status.size`
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=`.metadata.creationTimestamp`

// VirtualMachineImage is the Schema for the virtualmachineimages API
type VirtualMachineImage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMachineImageSpec   `json:"spec,omitempty"`
	Status VirtualMachineImageStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMachineImageList contains a list of VirtualMachineImage
type VirtualMachineImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMachineImage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VirtualMachineImage{}, &VirtualMachineImageList{})
}
