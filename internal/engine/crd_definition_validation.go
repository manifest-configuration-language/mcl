package engine

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
)

func validateRuntimeCustomResourceDefinition(ctx context.Context, value RuntimeValue) error {
	internal, err := runtimeCustomResourceDefinitionAsInternal(value)
	if err != nil {
		return err
	}
	// CRD作成時と同様にstatusを初期化し、storage versionを記録します。
	internal.Status = apiextensions.CustomResourceDefinitionStatus{}
	for _, version := range internal.Spec.Versions {
		if version.Storage {
			internal.Status.StoredVersions = []string{version.Name}

			break
		}
	}
	validationErrors := crdvalidation.ValidateCustomResourceDefinition(ctx, internal)
	if len(validationErrors) == 0 {
		return nil
	}

	messages := make([]string, len(validationErrors))
	for index, validationError := range validationErrors {
		messages[index] = validationError.Error()
	}

	return errors.New(strings.Join(messages, "; "))
}

func runtimeCustomResourceDefinitionAsInternal(value RuntimeValue) (*apiextensions.CustomResourceDefinition, error) {
	resource, err := runtimeValueAsKubernetesJSON(value)
	if err != nil {
		return nil, err
	}
	data, err := jsonv2.Marshal(resource)
	if err != nil {
		return nil, err
	}
	var versioned apiextensionsv1.CustomResourceDefinition
	if err := jsonv2.Unmarshal(data, &versioned); err != nil {
		return nil, err
	}
	apiextensionsv1.SetObjectDefaults_CustomResourceDefinition(&versioned)
	internal := new(apiextensions.CustomResourceDefinition)
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&versioned, internal, nil); err != nil {
		return nil, err
	}

	return internal, nil
}
