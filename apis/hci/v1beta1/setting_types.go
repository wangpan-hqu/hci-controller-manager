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
	SettingConfigured condition.Cond = "configured"
)

// SettingStatus defines the observed state of Setting
type SettingStatus struct {
	// +optional
	Conditions []Condition `json:"conditions,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="VALUE",type="string",JSONPath=`.value`

// Setting is the Schema for the settings API
type Setting struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Value string `json:"value,omitempty"`

	// +optional
	Default string `json:"default,omitempty"`

	// +optional
	Customized bool `json:"customized,omitempty"`

	// +optional
	Source string `json:"source,omitempty"`

	Status SettingStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SettingList contains a list of Setting
type SettingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Setting `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Setting{}, &SettingList{})
}
