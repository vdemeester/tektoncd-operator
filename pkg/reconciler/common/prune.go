/*
Copyright 2020 The Tekton Authors

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

package common

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tektoncd/operator/pkg/apis/operator/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	v1beta1 "k8s.io/api/batch/v1beta1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"knative.dev/pkg/logging"
)

const (
	openshiftPrefix  = "openshift"
	kubePrefix       = "kube"
	tektonSA         = "tekton-pipelines-controller"
	CronName         = "resource-pruner"
	JobsTKNImageName = "IMAGE_JOB_PRUNER_TKN"
	ownerAPIVer      = "operator.tekton.dev/v1alpha1"
	ownerKind        = "TektonConfig"
)

func Prune(k kubernetes.Interface, ctx context.Context, tC *v1alpha1.TektonConfig) error {
	if 0 < len(tC.Spec.Pruner.Resources) {
		tknImage := os.Getenv(JobsTKNImageName)
		if tknImage == "" {
			return fmt.Errorf("%s environment variable not found", JobsTKNImageName)
		}
		pru := tC.Spec.Pruner
		logger := logging.FromContext(ctx)
		ownerRef := v1.OwnerReference{
			APIVersion: ownerAPIVer,
			Kind:       ownerKind,
			Name:       tC.Name,
			UID:        tC.ObjectMeta.UID,
		}

		pruningNamespaces, err := GetPrunableNamespaces(k, ctx)
		if err != nil {
			return err
		}
		if err := createCronJob(k, ctx, pru, tC.Spec.TargetNamespace, pruningNamespaces, ownerRef, tknImage); err != nil {
			logger.Error("failed to create cronjob ", err)

		}
	}
	return nil
}

func GetPrunableNamespaces(k kubernetes.Interface, ctx context.Context) ([]string, error) {
	nsList, err := k.CoreV1().Namespaces().List(ctx, v1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var allNameSpaces []string
	for _, ns := range nsList.Items {
		if !strings.HasPrefix(ns.Name, openshiftPrefix) && !strings.HasPrefix(ns.Name, kubePrefix) {
			allNameSpaces = append(allNameSpaces, ns.Name)
		}
	}
	return allNameSpaces, nil
}

func createCronJob(k kubernetes.Interface, ctx context.Context, pru v1alpha1.Prune, targetNs string, pruningNs []string, oR v1.OwnerReference, tknImage string) error {
	pruneContainers := getPruningContainers(pru.Resources, pruningNs, pru.Keep, tknImage)
	backOffLimit := int32(3)
	ttlSecondsAfterFinished := int32(3600)
	cj := &v1beta1.CronJob{
		TypeMeta: v1.TypeMeta{
			Kind:       "CronJob",
			APIVersion: "batch/v1beta1",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:            CronName,
			OwnerReferences: []v1.OwnerReference{oR},
		},
		Spec: v1beta1.CronJobSpec{
			Schedule:          pru.Schedule,
			ConcurrencyPolicy: "Forbid",
			JobTemplate: v1beta1.JobTemplateSpec{

				Spec: batchv1.JobSpec{
					TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
					BackoffLimit:            &backOffLimit,

					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:         pruneContainers,
							RestartPolicy:      "OnFailure",
							ServiceAccountName: tektonSA,
						},
					},
				},
			},
		},
	}

	if _, err := k.BatchV1beta1().CronJobs(targetNs).Create(ctx, cj, v1.CreateOptions{}); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			if _, err := k.BatchV1beta1().CronJobs(targetNs).Update(ctx, cj, v1.UpdateOptions{}); err != nil {
				return err
			}
		}
		return err
	}
	return nil
}

func getPruningContainers(resources, namespaces []string, keep int, tknImage string) []corev1.Container {
	containers := []corev1.Container{}
	for _, ns := range namespaces {
		cmdArgs := deleteCommand(resources, keep, ns)
		cName := "pruner-tkn-" + ns
		container := corev1.Container{
			Name:                     cName,
			Image:                    tknImage,
			Command:                  []string{"/bin/sh", "-c"},
			Args:                     cmdArgs,
			TerminationMessagePolicy: "FallbackToLogsOnError",
		}
		containers = append(containers, container)
	}

	return containers
}

func deleteCommand(resources []string, keep int, ns string) []string {
	cmdArgs := []string{}
	for _, res := range resources {
		cmd := "tkn " + strings.ToLower(res) + " delete --keep " + fmt.Sprint(keep) + " -f -n " + ns
		cmdArgs = append(cmdArgs, cmd)
	}
	return cmdArgs
}
