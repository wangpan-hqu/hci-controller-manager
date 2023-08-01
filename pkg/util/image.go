package util

import (
	"fmt"

	longhorntypes "github.com/longhorn/longhorn-manager/types"

	hciv1 "git.netfuse.cn/algo/hci-controller-manager/apis/hci/v1beta1"
)

func GetBackingImageName(image *hciv1.VirtualMachineImage) string {
	return fmt.Sprintf("%s-%s", image.Namespace, image.Name)
}

func GetImageStorageClassName(imageName string) string {
	return fmt.Sprintf("longhorn-%s", imageName)
}

func GetImageStorageClassParameters(image *hciv1.VirtualMachineImage) map[string]string {
	params := map[string]string{
		LonghornOptionBackingImageName: GetBackingImageName(image),
	}
	for k, v := range image.Spec.StorageClassParameters {
		params[k] = v
	}
	return params
}

func GetImageDefaultStorageClassParameters() map[string]string {
	return map[string]string{
		longhorntypes.OptionNumberOfReplicas:    "3",
		longhorntypes.OptionStaleReplicaTimeout: "30",
		LonghornOptionMigratable:                "true",
	}
}
