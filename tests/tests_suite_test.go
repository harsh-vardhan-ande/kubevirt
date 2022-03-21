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
 * Copyright 2017 Red Hat, Inc.
 *
 */

package tests_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	ginkgo_reporters "github.com/onsi/ginkgo/v2/reporters"

	"kubevirt.io/kubevirt/tests/flags"
	"kubevirt.io/kubevirt/tests/reporter"

	v1reporter "kubevirt.io/client-go/reporter"
	qe_reporters "kubevirt.io/qe-tools/pkg/ginkgo-reporters"

	"kubevirt.io/kubevirt/tests"
	vmsgeneratorutils "kubevirt.io/kubevirt/tools/vms-generator/utils"

	_ "kubevirt.io/kubevirt/tests/launchsecurity"
	_ "kubevirt.io/kubevirt/tests/monitoring"
	_ "kubevirt.io/kubevirt/tests/network"
	_ "kubevirt.io/kubevirt/tests/numa"
	_ "kubevirt.io/kubevirt/tests/performance"
	_ "kubevirt.io/kubevirt/tests/realtime"
	_ "kubevirt.io/kubevirt/tests/storage"
)

var afterSuiteReporters = []Reporter{}
var k8sReporter *reporter.KubernetesReporter

func TestTests(t *testing.T) {
	flags.NormalizeFlags()
	tests.CalculateNamespaces()
	maxFails := getMaxFailsFromEnv()
	artifactsPath := filepath.Join(flags.ArtifactsDir, "k8s-reporter")
	junitOutput := filepath.Join(flags.ArtifactsDir, "junit.functest.xml")
	if qe_reporters.JunitOutput != "" {
		junitOutput = qe_reporters.JunitOutput
	}

	suiteConfig, _ := GinkgoConfiguration()
	if suiteConfig.ParallelTotal > 1 {
		artifactsPath = filepath.Join(artifactsPath, strconv.Itoa(GinkgoParallelProcess()))
		junitOutput = filepath.Join(flags.ArtifactsDir, fmt.Sprintf("partial.junit.functest.%d.xml", GinkgoParallelProcess()))
	}

	outputEnricherReporter := reporter.NewCapturedOutputEnricher(
		v1reporter.NewV1JUnitReporter(junitOutput),
	)
	afterSuiteReporters = append(afterSuiteReporters, outputEnricherReporter)

	if qe_reporters.Polarion.Run {
		if suiteConfig.ParallelTotal > 1 {
			qe_reporters.Polarion.Filename = filepath.Join(flags.ArtifactsDir, fmt.Sprintf("partial.polarion.functest.%d.xml", GinkgoParallelProcess()))
		}
		afterSuiteReporters = append(afterSuiteReporters, &qe_reporters.Polarion)
	}

	k8sReporter = reporter.NewKubernetesReporter(artifactsPath, maxFails)
	k8sReporter.Cleanup()

	vmsgeneratorutils.DockerPrefix = flags.KubeVirtUtilityRepoPrefix
	vmsgeneratorutils.DockerTag = flags.KubeVirtVersionTag

	RunSpecs(t, "Tests Suite")
}

var _ = SynchronizedBeforeSuite(tests.SynchronizedBeforeTestSetup, tests.BeforeTestSuitSetup)

var _ = SynchronizedAfterSuite(tests.AfterTestSuitCleanup, tests.SynchronizedAfterTestSuiteCleanup)

func getMaxFailsFromEnv() int {
	maxFailsEnv := os.Getenv("REPORTER_MAX_FAILS")
	if maxFailsEnv == "" {
		return 10
	}

	maxFails, err := strconv.Atoi(maxFailsEnv)
	if err != nil { // if the variable is set with a non int value
		fmt.Println("Invalid REPORTER_MAX_FAILS variable, defaulting to 10")
		return 10
	}

	return maxFails
}

var _ = ReportAfterSuite("TestTests", func(report Report) {
	for _, reporter := range afterSuiteReporters {
		ginkgo_reporters.ReportViaDeprecatedReporter(reporter, report)
	}
})

var _ = ReportBeforeEach(func(specReport SpecReport) {
	k8sReporter.JustBeforeEach(CurrentSpecReport())
})

var _ = ReportAfterEach(func(specReport SpecReport) {
	k8sReporter.JustAfterEach(CurrentSpecReport())
})
