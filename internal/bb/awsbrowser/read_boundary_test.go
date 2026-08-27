package awsbrowser

import (
	"reflect"
	"testing"
)

func TestSDKRuntimeHasNoPublicEscapeHatches(t *testing.T) {
	typeOf := reflect.TypeOf(sdkRuntime{})
	for i := 0; i < typeOf.NumField(); i++ {
		field := typeOf.Field(i)
		if field.IsExported() {
			t.Errorf("exported sdkRuntime field %s exposes %v", field.Name, field.Type)
		}
	}

	approved := map[string]bool{
		"EC2":      true,
		"IAM":      true,
		"Identity": true,
		"Route53":  true,
		"STS":      true,
	}
	runtimeContract := reflect.TypeOf((*RuntimeContext)(nil)).Elem()
	for i := 0; i < runtimeContract.NumMethod(); i++ {
		method := runtimeContract.Method(i)
		if !approved[method.Name] {
			t.Errorf("unapproved RuntimeContext method %s", method.Name)
		}
		delete(approved, method.Name)
	}
	for missing := range approved {
		t.Errorf("missing RuntimeContext method %s", missing)
	}
}
