/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

package fv_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	extensionv1alpha1 "github.com/gianlucam76/jsonnet-controller/api/v1alpha1"
)

// ConfigMap jsonnet contains
/*
local replicas = std.extVar("replicas");
local deploymentName = std.extVar("deploymentName");
local namespace = std.extVar("namespace");

{
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: {
    name: deploymentName,
    labels: {
      app: deploymentName
    },
    namespace: namespace
  },
  spec: {
    replicas: replicas,
    selector: {
      matchLabels: {
        app: deploymentName
      }
    },
    template: {
      metadata: {
        labels: {
          app: deploymentName
        }
      },
      spec: {
        containers: [
          {
            name: 'my-container',
            image: 'nginx:latest',
            ports: [
              {
                containerPort: 80
              }
            ]
          }
        ]
      }
    }
  }
}

// ConfigMap with import contains
deployment.jsonnet
service.jsonnet

main.jsonnet
local deployment = import 'deployment.jsonnet';
local service = import 'service.jsonnet';

{
  "deployment": deployment,
  "service": service
}
*/

var _ = Describe("Jsonnet", Serial, func() {
	const (
		namePrefix                        = "jsonnet-cm-"
		jsonnetConfigMapNameWithVariables = "jsonnet-with-variables"
		jsonnetConfigWithImport           = "jsonnet-with-import"
	)

	It("Process a ConfigMap with Jsonnet files", Label("FV"), func() {
		verifyJsonnetSourceWithConfigMap(namePrefix, jsonnetConfigMapNameWithVariables, "deployment.jsonnet", 1)
		verifyJsonnetSourceWithConfigMap(namePrefix, jsonnetConfigWithImport, "main.jsonnet", 2)
	})
})

func verifyJsonnetSourceWithConfigMap(namePrefix, jsonnetConfigMapName, jsonnetFileName string, expectedResources int) {
	Byf("Verifying ConfigMap %s exists. It is created by Makefile", jsonnetConfigMapName)
	jsonnetConfigMap := &corev1.ConfigMap{}
	Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: defaultNamespace, Name: jsonnetConfigMapName},
		jsonnetConfigMap)).To(Succeed())

	Expect("Verifying ConfigMap %s contains jsonnet.tar.gz", jsonnetConfigMapName)
	Expect(jsonnetConfigMap.BinaryData).ToNot(BeNil())
	_, ok := jsonnetConfigMap.BinaryData["jsonnet.tar.gz"]
	Expect(ok).To(BeTrue())

	Byf("Creating a JsonnetSource referencing this ConfigMap")
	jsonnetSource := &extensionv1alpha1.JsonnetSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namePrefix + randomString(),
			Namespace: randomString(),
		},
		Spec: extensionv1alpha1.JsonnetSourceSpec{
			Namespace: jsonnetConfigMap.Namespace,
			Name:      jsonnetConfigMap.Name,
			Kind:      configMapKind,
			Path:      jsonnetFileName,
			Variables: map[string]string{
				"deploymentName": randomString(),
				"namespace":      randomString(),
				"replicas":       "3",
			},
		},
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: jsonnetSource.Namespace,
		},
	}
	Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

	Expect(k8sClient.Create(context.TODO(), jsonnetSource)).To(Succeed())

	Byf("Verifying JsonnetSource %s/%s Status", jsonnetSource.Namespace, jsonnetSource.Name)
	Eventually(func() bool {
		currentJsonnetSource := &extensionv1alpha1.JsonnetSource{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: jsonnetSource.Namespace, Name: jsonnetSource.Name},
			currentJsonnetSource)
		if err != nil {
			return false
		}
		if currentJsonnetSource.Status.FailureMessage != nil {
			return false
		}
		if currentJsonnetSource.Status.Resources == "" {
			return false
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Verifying JsonnetSource %s/%s Status.Resources", jsonnetSource.Namespace, jsonnetSource.Name)

	currentJsonnetSource := &extensionv1alpha1.JsonnetSource{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: jsonnetSource.Namespace, Name: jsonnetSource.Name},
		currentJsonnetSource)).To(Succeed())

	resources := collectContent(currentJsonnetSource.Status.Resources)
	Expect(len(resources)).To(Equal(expectedResources))

	// Expect(k8sClient.Delete(context.TODO(), ns)).To(Succeed())
}
