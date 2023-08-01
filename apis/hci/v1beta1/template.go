package v1beta1

import (
	"github.com/wangpan-hqu/hci-controller-manager/pkg/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

var (
	VersionAssigned condition.Cond = "assigned" // version number was assigned to templateVersion object's status.Version
)

type VirtualMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMachineTemplateSpec   `json:"spec,omitempty"`
	Status VirtualMachineTemplateStatus `json:"status,omitempty"`
}

type VirtualMachineTemplateSpec struct {
	// +optional
	DefaultVersionID string `json:"defaultVersionId"`

	// +optional
	Description string `json:"description,omitempty"`
}

type VirtualMachineTemplateStatus struct {
	// +optional
	DefaultVersion int `json:"defaultVersion,omitempty"`

	// +optional
	LatestVersion int `json:"latestVersion,omitempty"`
}

type VirtualMachineTemplateVersion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMachineTemplateVersionSpec   `json:"spec"`
	Status VirtualMachineTemplateVersionStatus `json:"status,omitempty"`
}

type VirtualMachineTemplateVersionSpec struct {
	// +kubebuilder:validation:Required
	TemplateID string `json:"templateId"`

	// +optional
	Description string `json:"description,omitempty"`

	// +optional
	ImageID string `json:"imageId,omitempty"`

	// +optional
	KeyPairIDs []string `json:"keyPairIds,omitempty"`

	// +optional
	VM VirtualMachineSourceSpec `json:"vm,omitempty"`
}

type VirtualMachineSourceSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec kubevirtv1.VirtualMachineSpec `json:"spec,omitempty"`
}

type VirtualMachineTemplateVersionStatus struct {
	// +optional
	Version int `json:"version,omitempty"`

	// +optional
	Conditions []Condition `json:"conditions,omitempty"`
}
