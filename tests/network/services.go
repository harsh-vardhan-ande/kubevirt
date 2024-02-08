/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2020 Red Hat, Inc.
 *
 */

package network

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"kubevirt.io/kubevirt/tests/framework/kubevirt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"kubevirt.io/kubevirt/tests/util"

	batchv1 "k8s.io/api/batch/v1"
	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"

	"kubevirt.io/kubevirt/tests/console"
	"kubevirt.io/kubevirt/tests/libnet"
	"kubevirt.io/kubevirt/tests/libnet/job"
	netservice "kubevirt.io/kubevirt/tests/libnet/service"
	"kubevirt.io/kubevirt/tests/libnet/vmnetserver"
	"kubevirt.io/kubevirt/tests/libvmi"
	"kubevirt.io/kubevirt/tests/libwait"
)

const (
	cleaningK8sv1ServiceShouldSucceed  = "cleaning up the k8sv1.Service entity should have succeeded."
	cleaningK8sv1JobShouldSucceed      = "cleaning up the k8sv1.Job entity should have succeeded."
	expectConnectivityToExposedService = "connectivity is expected to the exposed service"

	jobSuccessRetry = 3
	jobFailureRetry = 0
)

var _ = SIGDescribe("Services", func() {
	var virtClient kubecli.KubevirtClient

	exposeExistingVMISpec := func(vmi *v1.VirtualMachineInstance, subdomain string, hostname string, selectorLabelKey string, selectorLabelValue string) *v1.VirtualMachineInstance {
		vmi.Labels = map[string]string{selectorLabelKey: selectorLabelValue}
		vmi.Spec.Subdomain = subdomain
		vmi.Spec.Hostname = hostname

		return vmi
	}

	readyVMI := func(vmi *v1.VirtualMachineInstance, loginTo console.LoginToFunction) *v1.VirtualMachineInstance {
		createdVMI, err := virtClient.VirtualMachineInstance(util.NamespaceTestDefault).Create(context.Background(), vmi)
		Expect(err).ToNot(HaveOccurred())

		return libwait.WaitUntilVMIReady(createdVMI, loginTo)
	}

	cleanupVMI := func(virtClient kubecli.KubevirtClient, vmi *v1.VirtualMachineInstance) {
		By("Deleting the VMI")
		Expect(virtClient.VirtualMachineInstance(util.NamespaceTestDefault).Delete(context.Background(), vmi.GetName(), &k8smetav1.DeleteOptions{})).To(Succeed())

		By("Waiting for the VMI to be gone")
		Eventually(func() error {
			_, err := virtClient.VirtualMachineInstance(util.NamespaceTestDefault).Get(context.Background(), vmi.GetName(), &k8smetav1.GetOptions{})
			return err
		}, 2*time.Minute, time.Second).Should(SatisfyAll(HaveOccurred(), WithTransform(errors.IsNotFound, BeTrue())), "The VMI should be gone within the given timeout")
	}

	cleanupService := func(namespace string, serviceName string) error {
		return virtClient.CoreV1().Services(namespace).Delete(context.Background(), serviceName, k8smetav1.DeleteOptions{})
	}

	BeforeEach(func() {
		virtClient = kubevirt.Client()
	})

	Context("bridge interface binding", func() {
		var inboundVMI *v1.VirtualMachineInstance

		const (
			selectorLabelKey   = "expose"
			selectorLabelValue = "me"
			servicePort        = 1500
		)

		createVMISpecWithBridgeInterface := func() *v1.VirtualMachineInstance {
			return libvmi.NewCirros(
				libvmi.WithInterface(libvmi.InterfaceDeviceWithBridgeBinding(v1.DefaultPodNetwork().Name)),
				libvmi.WithNetwork(v1.DefaultPodNetwork()))
		}

		createReadyVMIWithBridgeBindingAndExposedService := func(hostname string, subdomain string) *v1.VirtualMachineInstance {
			return readyVMI(
				exposeExistingVMISpec(
					createVMISpecWithBridgeInterface(), subdomain, hostname, selectorLabelKey, selectorLabelValue),
				console.LoginToCirros)
		}

		BeforeEach(func() {
			libnet.SkipWhenClusterNotSupportIpv4()
			subdomain := "vmi"
			hostname := "inbound"

			inboundVMI = createReadyVMIWithBridgeBindingAndExposedService(hostname, subdomain)
			vmnetserver.StartTCPServer(inboundVMI, servicePort, console.LoginToCirros)
		})

		AfterEach(func() {
			Expect(inboundVMI).NotTo(BeNil(), "the VMI object must exist in order to be deleted.")
			cleanupVMI(virtClient, inboundVMI)
		})

		Context("with a service matching the vmi exposed", func() {
			const serviceName = "myservice"

			BeforeEach(func() {
				service := netservice.BuildSpec(serviceName, servicePort, servicePort, selectorLabelKey, selectorLabelValue)
				serv, err := virtClient.CoreV1().Services(inboundVMI.Namespace).Create(context.Background(), service, k8smetav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(func() {
					err := virtClient.CoreV1().Services(serv.Namespace).Delete(context.Background(), serv.Name, k8smetav1.DeleteOptions{})
					Expect(err).To(SatisfyAny(
						Not(HaveOccurred()),
						MatchError(errors.IsNotFound, "does not exist"),
					), cleaningK8sv1ServiceShouldSucceed)
				})
			})

			It("[test_id:1547] should be able to reach the vmi based on labels specified on the vmi", func() {
				tcpJob, err := createServiceConnectivityJob(serviceName, inboundVMI.Namespace, servicePort, jobSuccessRetry)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() {
					Expect(virtClient.BatchV1().Jobs(tcpJob.Namespace).Delete(context.Background(), tcpJob.Name, k8smetav1.DeleteOptions{})).To(Succeed())
				})

				Expect(job.WaitForJobToSucceed(tcpJob, 90*time.Second)).To(Succeed(), expectConnectivityToExposedService)
			})

			It("[test_id:1548] should fail to reach the vmi if an invalid servicename is used", func() {
				tcpJob, err := createServiceConnectivityJob("wrongservice", inboundVMI.Namespace, servicePort, jobFailureRetry)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() {
					err := virtClient.BatchV1().Jobs(tcpJob.Namespace).Delete(context.Background(), tcpJob.Name, k8smetav1.DeleteOptions{})
					Expect(err).To(SatisfyAny(
						Not(HaveOccurred()),
						MatchError(errors.IsNotFound, "does not exist"),
					))
				})

				err = job.WaitForJobToFail(tcpJob, 90*time.Second)
				Expect(err).NotTo(HaveOccurred(), "connectivity is *not* expected, since there isn't an exposed service")
			})
		})

		Context("with a subdomain and a headless service given", func() {
			var serviceName string
			BeforeEach(func() {
				serviceName = inboundVMI.Spec.Subdomain

				service := netservice.BuildHeadlessSpec(serviceName, servicePort, servicePort, selectorLabelKey, selectorLabelValue)
				_, err := virtClient.CoreV1().Services(inboundVMI.Namespace).Create(context.Background(), service, k8smetav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				Expect(virtClient.CoreV1().Services(inboundVMI.Namespace).Delete(context.Background(), serviceName, k8smetav1.DeleteOptions{})).To(Succeed())
			})

			It("[test_id:1549]should be able to reach the vmi via its unique fully qualified domain name", func() {
				var err error
				serviceHostnameWithSubdomain := fmt.Sprintf("%s.%s", inboundVMI.Spec.Hostname, inboundVMI.Spec.Subdomain)

				tcpJob, err := createServiceConnectivityJob(serviceHostnameWithSubdomain, inboundVMI.Namespace, servicePort, jobSuccessRetry)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() {
					Expect(virtClient.BatchV1().Jobs(tcpJob.Namespace).Delete(context.Background(), tcpJob.Name, k8smetav1.DeleteOptions{})).To(
						Succeed(),
						cleaningK8sv1JobShouldSucceed,
					)
				})

				Expect(job.WaitForJobToSucceed(tcpJob, 90*time.Second)).To(Succeed(), expectConnectivityToExposedService)
			})
		})
	})

	Context("Masquerade interface binding", func() {
		var inboundVMI *v1.VirtualMachineInstance

		const (
			selectorLabelKey   = "expose"
			selectorLabelValue = "me"
			servicePort        = 1500
		)

		createReadyVMIWithMasqueradeBindingAndExposedService := func(hostname string, subdomain string) *v1.VirtualMachineInstance {
			vmi := libvmi.NewFedora(
				libnet.WithMasqueradeNetworking()...,
			)
			return readyVMI(
				exposeExistingVMISpec(vmi, subdomain, hostname, selectorLabelKey, selectorLabelValue),
				console.LoginToFedora)
		}

		BeforeEach(func() {
			const subdomain = "vmi"
			const hostname = "inbound"

			inboundVMI = createReadyVMIWithMasqueradeBindingAndExposedService(hostname, subdomain)
			vmnetserver.StartTCPServer(inboundVMI, servicePort, console.LoginToFedora)
		})

		AfterEach(func() {
			Expect(inboundVMI).NotTo(BeNil(), "the VMI object must exist in order to be deleted.")
			cleanupVMI(virtClient, inboundVMI)
		})

		Context("with a service matching the vmi exposed", func() {
			var service *k8sv1.Service

			AfterEach(func() {
				Expect(cleanupService(inboundVMI.GetNamespace(), service.Name)).To(Succeed(), cleaningK8sv1ServiceShouldSucceed)
			})

			DescribeTable("[Conformance] should be able to reach the vmi based on labels specified on the vmi", func(ipFamily k8sv1.IPFamily) {
				serviceName := "myservice"
				By("setting up resources to expose the VMI via a service", func() {
					libnet.SkipWhenClusterNotSupportIPFamily(ipFamily)
					if ipFamily == k8sv1.IPv6Protocol {
						serviceName += "v6"
						service = netservice.BuildIPv6Spec(serviceName, servicePort, servicePort, selectorLabelKey, selectorLabelValue)
					} else {
						service = netservice.BuildSpec(serviceName, servicePort, servicePort, selectorLabelKey, selectorLabelValue)
					}

					_, err := virtClient.CoreV1().Services(inboundVMI.Namespace).Create(context.Background(), service, k8smetav1.CreateOptions{})
					Expect(err).NotTo(HaveOccurred(), "the k8sv1.Service entity should have been created.")
				})

				By("checking connectivity the exposed service")
				tcpJob, err := createServiceConnectivityJob(serviceName, inboundVMI.Namespace, servicePort, jobSuccessRetry)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() {
					err := virtClient.BatchV1().Jobs(tcpJob.Namespace).Delete(context.Background(), tcpJob.Name, k8smetav1.DeleteOptions{})
					Expect(err).To(SatisfyAny(
						Not(HaveOccurred()),
						MatchError(errors.IsNotFound, "does not exist"),
					), cleaningK8sv1JobShouldSucceed)
				})

				Expect(job.WaitForJobToSucceed(tcpJob, 90*time.Second)).To(Succeed(), expectConnectivityToExposedService)
			},
				Entry("when the service is exposed by an IPv4 address.", k8sv1.IPv4Protocol),
				Entry("when the service is exposed by an IPv6 address.", k8sv1.IPv6Protocol),
			)
		})

		Context("*without* a service matching the vmi exposed", func() {
			It("should fail to reach the vmi", func() {
				tcpJob, err := createServiceConnectivityJob("missingservice", inboundVMI.Namespace, servicePort, jobFailureRetry)
				Expect(err).NotTo(HaveOccurred())

				DeferCleanup(func() {
					err := virtClient.BatchV1().Jobs(tcpJob.Namespace).Delete(context.Background(), tcpJob.Name, k8smetav1.DeleteOptions{})
					Expect(err).To(SatisfyAny(
						Not(HaveOccurred()),
						MatchError(errors.IsNotFound, "does not exist"),
					))
				})

				err = job.WaitForJobToFail(tcpJob, 90*time.Second)
				Expect(err).NotTo(HaveOccurred(), "connectivity is *not* expected, since there isn't an exposed service")
			})
		})
	})
})

func createServiceConnectivityJob(serviceName, namespace string, servicePort int, retries int32) (*batchv1.Job, error) {
	serviceFQDN := fmt.Sprintf("%s.%s", serviceName, namespace)

	By(fmt.Sprintf("starting a job which tries to reach the vmi via service %s, on port %d", serviceFQDN, servicePort))
	tcpJob := job.NewHelloWorldJobTCP(serviceFQDN, strconv.Itoa(servicePort))
	tcpJob.Spec.BackoffLimit = &retries
	return kubevirt.Client().BatchV1().Jobs(namespace).Create(context.Background(), tcpJob, k8smetav1.CreateOptions{})
}
