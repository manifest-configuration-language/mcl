package main

import "testing"

func TestDiscoverResourcesUsesCollectionRoutesAndListGVKs(t *testing.T) {
	t.Parallel()

	spec := specification{
		Paths: map[string]pathItem{
			"/api/v1/pods": {
				Get: operation{Responses: map[string]response{"200": {Schema: schemaReference{Ref: "#/definitions/PodList"}}}},
			},
			"/api/v1/namespaces/{namespace}/pods": {
				Get: operation{Responses: map[string]response{"200": {Schema: schemaReference{Ref: "#/definitions/PodList"}}}},
			},
			"/api/v1/nodes": {
				Get: operation{Responses: map[string]response{"200": {Schema: schemaReference{Ref: "#/definitions/NodeList"}}}},
			},
			"/apis/apps/v1/deployments": {
				Get: operation{Responses: map[string]response{"200": {Schema: schemaReference{Ref: "#/definitions/DeploymentList"}}}},
			},
			"/apis/apps/v1/namespaces/{namespace}/deployments": {
				Get: operation{Responses: map[string]response{"200": {Schema: schemaReference{Ref: "#/definitions/DeploymentList"}}}},
			},
			"/apis/apps/v1/deployments/{name}/scale": {
				Get: operation{Responses: map[string]response{"200": {Schema: schemaReference{Ref: "#/definitions/Scale"}}}},
			},
		},
		Definitions: map[string]definition{
			"PodList":        {GroupVersionKinds: []groupVersionKind{{Version: "v1", Kind: "PodList"}}},
			"NodeList":       {GroupVersionKinds: []groupVersionKind{{Version: "v1", Kind: "NodeList"}}},
			"DeploymentList": {GroupVersionKinds: []groupVersionKind{{Group: "apps", Version: "v1", Kind: "DeploymentList"}}},
		},
	}

	resources, err := discoverResources(spec)
	if err != nil {
		t.Fatalf("discoverResources() error = %v", err)
	}

	for _, test := range []struct {
		key        resourceKey
		name       string
		namespaced bool
	}{
		{key: resourceKey{version: "v1", kind: "Pod"}, name: "pods", namespaced: true},
		{key: resourceKey{version: "v1", kind: "Node"}, name: "nodes"},
		{key: resourceKey{group: "apps", version: "v1", kind: "Deployment"}, name: "deployments", namespaced: true},
	} {
		got, ok := resources[test.key]
		if !ok {
			t.Errorf("discoverResources() missing descriptor for %+v", test.key)
			continue
		}
		if got.name != test.name || got.namespaced != test.namespaced {
			t.Errorf("discoverResources()[%+v] = %+v, want name=%q namespaced=%t", test.key, got, test.name, test.namespaced)
		}
	}
	if len(resources) != 3 {
		t.Errorf("discoverResources() returned %d resources, want 3", len(resources))
	}
}
