```
# init
kubebuilder init --domain wjyl.com --repo git.netfuse.cn/algo/hci-controller-manager

# add mutligroup to PROJECT
multigroup: true

# create api
kubebuilder create api --namespaced true --group hci --version v1beta1 --kind VirtualMachineImage
kubebuilder create api --namespaced true --group hci --version v1beta1 --kind VirtualMachineBackup
kubebuilder create api --namespaced true --group hci --version v1beta1 --kind VirtualMachineRestore
kubebuilder create api --namespaced true --group hci --version v1beta1 --kind Setting

```