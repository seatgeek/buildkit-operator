package install

import (
	"github.com/seatgeek/buildkit-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func Install(scheme *runtime.Scheme) {
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
}
