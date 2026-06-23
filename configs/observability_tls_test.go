// Copyright (c) 2025 Beijing Volcano Engine Technology Co., Ltd. and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package configs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/volcengine/veadk-go/common"
)

func TestObservabilityConfig_ResolvesTLSTopicIDFromProjectAndService(t *testing.T) {
	t.Setenv(EnvObservabilityOpenTelemetryTLSEndpoint, "https://tls-cn-wuhu.volces.com:4318/v1/traces")
	t.Setenv(EnvObservabilityOpenTelemetryTLSProjectName, "tls-copilot-new-arch-traces")
	t.Setenv(EnvObservabilityOpenTelemetryTLSServiceName, "tls-copilot-new-arch-agentkit")
	t.Setenv(EnvObservabilityOpenTelemetryTLSRegion, "cn-wuhu")
	t.Setenv(common.VOLCENGINE_ACCESS_KEY, "ak-test")
	t.Setenv(common.VOLCENGINE_SECRET_KEY, "sk-test")
	t.Setenv(EnvObservabilityOpenTelemetryTLSTopicID, "")

	original := lookupObservabilityTLSTopicID
	t.Cleanup(func() { lookupObservabilityTLSTopicID = original })
	lookupObservabilityTLSTopicID = func(req observabilityTLSTopicLookupRequest) (string, error) {
		assert.Equal(t, "tls-copilot-new-arch-traces", req.ProjectName)
		assert.Equal(t, "tls-copilot-new-arch-agentkit", req.ServiceName)
		assert.Equal(t, "cn-wuhu", req.Region)
		assert.Equal(t, "ak-test", req.AccessKey)
		assert.Equal(t, "sk-test", req.SecretKey)
		return "trace-topic-123", nil
	}

	config := &ObservabilityConfig{}
	config.MapEnvToConfig()

	if assert.NotNil(t, config.OpenTelemetry) && assert.NotNil(t, config.OpenTelemetry.TLS) {
		assert.Equal(t, "trace-topic-123", config.OpenTelemetry.TLS.TopicID)
		assert.Equal(t, "ak-test", config.OpenTelemetry.TLS.AccessKey)
		assert.Equal(t, "sk-test", config.OpenTelemetry.TLS.SecretKey)
	}
}

func TestObservabilityConfig_PrefersExplicitTLSTopicID(t *testing.T) {
	t.Setenv(EnvObservabilityOpenTelemetryTLSProjectName, "tls-copilot-new-arch-traces")
	t.Setenv(EnvObservabilityOpenTelemetryTLSServiceName, "tls-copilot-new-arch-agentkit")
	t.Setenv(EnvObservabilityOpenTelemetryTLSRegion, "cn-wuhu")
	t.Setenv(EnvObservabilityOpenTelemetryTLSTopicID, "trace-topic-existing")

	original := lookupObservabilityTLSTopicID
	t.Cleanup(func() { lookupObservabilityTLSTopicID = original })
	lookupObservabilityTLSTopicID = func(req observabilityTLSTopicLookupRequest) (string, error) {
		t.Fatalf("lookupObservabilityTLSTopicID should not be called when topic id is already configured: %+v", req)
		return "", nil
	}

	config := &ObservabilityConfig{}
	config.MapEnvToConfig()

	if assert.NotNil(t, config.OpenTelemetry) && assert.NotNil(t, config.OpenTelemetry.TLS) {
		assert.Equal(t, "trace-topic-existing", config.OpenTelemetry.TLS.TopicID)
	}
}
