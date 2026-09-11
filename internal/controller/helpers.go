package controller

import "k8s.io/apimachinery/pkg/util/intstr"

func intOrString(port int) intstr.IntOrString {
	return intstr.FromInt32(int32(port))
}
