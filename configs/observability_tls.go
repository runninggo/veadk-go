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
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/volcengine/veadk-go/common"
	tlssdk "github.com/volcengine/volc-sdk-golang/service/tls"
)

type observabilityTLSTopicLookupRequest struct {
	ProjectName string
	ServiceName string
	Region      string
	Endpoint    string
	AccessKey   string
	SecretKey   string
}

var lookupObservabilityTLSTopicID = defaultLookupObservabilityTLSTopicID

func resolveObservabilityTLSTopicIDFromEnv() (string, error) {
	if topicID := strings.TrimSpace(os.Getenv(EnvObservabilityOpenTelemetryTLSTopicID)); topicID != "" {
		return topicID, nil
	}

	projectName := strings.TrimSpace(os.Getenv(EnvObservabilityOpenTelemetryTLSProjectName))
	serviceName := strings.TrimSpace(os.Getenv(EnvObservabilityOpenTelemetryTLSServiceName))
	region := strings.TrimSpace(os.Getenv(EnvObservabilityOpenTelemetryTLSRegion))
	accessKey := strings.TrimSpace(os.Getenv(EnvObservabilityOpenTelemetryTLSAccessKey))
	secretKey := strings.TrimSpace(os.Getenv(EnvObservabilityOpenTelemetryTLSSecretKey))
	if accessKey == "" {
		accessKey = strings.TrimSpace(os.Getenv(common.VOLCENGINE_ACCESS_KEY))
	}
	if secretKey == "" {
		secretKey = strings.TrimSpace(os.Getenv(common.VOLCENGINE_SECRET_KEY))
	}
	endpoint := strings.TrimSpace(os.Getenv(EnvObservabilityOpenTelemetryTLSEndpoint))

	if projectName == "" || serviceName == "" || region == "" || accessKey == "" || secretKey == "" {
		return "", nil
	}

	topicID, err := lookupObservabilityTLSTopicID(observabilityTLSTopicLookupRequest{
		ProjectName: projectName,
		ServiceName: serviceName,
		Region:      region,
		Endpoint:    endpoint,
		AccessKey:   accessKey,
		SecretKey:   secretKey,
	})
	if err != nil {
		return "", fmt.Errorf("resolve %s from project %q failed: %w", EnvObservabilityOpenTelemetryTLSTopicID, projectName, err)
	}
	return strings.TrimSpace(topicID), nil
}

func defaultLookupObservabilityTLSTopicID(req observabilityTLSTopicLookupRequest) (string, error) {
	projectName := strings.TrimSpace(req.ProjectName)
	serviceName := strings.TrimSpace(req.ServiceName)
	region := strings.TrimSpace(req.Region)
	accessKey := strings.TrimSpace(req.AccessKey)
	secretKey := strings.TrimSpace(req.SecretKey)
	endpoint := normalizeObservabilityTLSLookupEndpoint(region, req.Endpoint)
	if projectName == "" || serviceName == "" || region == "" || endpoint == "" || accessKey == "" || secretKey == "" {
		return "", nil
	}

	client := tlssdk.NewClient(endpoint, accessKey, secretKey, "", region)
	projectResp, err := client.DescribeProjects(&tlssdk.DescribeProjectsRequest{
		ProjectName: projectName,
		IsFullName:  true,
		PageNumber:  1,
		PageSize:    100,
	})
	if err != nil {
		return "", err
	}

	projectID := ""
	for _, project := range projectResp.Projects {
		if strings.TrimSpace(project.ProjectName) == projectName {
			projectID = strings.TrimSpace(project.ProjectID)
			break
		}
	}
	if projectID == "" {
		return "", fmt.Errorf("project %q not found", projectName)
	}

	topicName := observabilityTLSTraceTopicName(serviceName)
	topicResp, err := client.DescribeTopics(&tlssdk.DescribeTopicsRequest{
		ProjectID:  projectID,
		TopicName:  topicName,
		IsFullName: true,
		PageNumber: 1,
		PageSize:   100,
	})
	if err != nil {
		return "", err
	}
	for _, topic := range topicResp.Topics {
		if topic == nil {
			continue
		}
		if strings.TrimSpace(topic.TopicName) == topicName && strings.TrimSpace(topic.TopicID) != "" {
			return strings.TrimSpace(topic.TopicID), nil
		}
	}
	return "", fmt.Errorf("trace topic %q not found in project %q", topicName, projectName)
}

func observabilityTLSTraceTopicName(serviceName string) string {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return ""
	}
	return "tls_" + serviceName + "-trace"
}

func normalizeObservabilityTLSLookupEndpoint(region, rawEndpoint string) string {
	rawEndpoint = strings.TrimSpace(rawEndpoint)
	if rawEndpoint == "" {
		region = strings.TrimSpace(region)
		if region == "" {
			return ""
		}
		return "https://tls-" + region + ".volces.com"
	}

	parsed, err := url.Parse(rawEndpoint)
	if err == nil && strings.TrimSpace(parsed.Host) != "" {
		scheme := strings.TrimSpace(parsed.Scheme)
		if scheme == "" {
			scheme = "https"
		}
		host := strings.TrimSpace(parsed.Hostname())
		if host == "" {
			host = strings.TrimSpace(parsed.Host)
		}
		return scheme + "://" + host
	}

	if strings.HasPrefix(rawEndpoint, "tls-") {
		return "https://" + rawEndpoint
	}
	return rawEndpoint
}
