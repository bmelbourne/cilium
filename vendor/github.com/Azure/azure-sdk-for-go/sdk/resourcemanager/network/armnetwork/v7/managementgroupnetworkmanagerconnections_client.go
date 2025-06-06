// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See License.txt in the project root for license information.
// Code generated by Microsoft (R) AutoRest Code Generator. DO NOT EDIT.
// Changes may cause incorrect behavior and will be lost if the code is regenerated.

package armnetwork

import (
	"context"
	"errors"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ManagementGroupNetworkManagerConnectionsClient contains the methods for the ManagementGroupNetworkManagerConnections group.
// Don't use this type directly, use NewManagementGroupNetworkManagerConnectionsClient() instead.
type ManagementGroupNetworkManagerConnectionsClient struct {
	internal *arm.Client
}

// NewManagementGroupNetworkManagerConnectionsClient creates a new instance of ManagementGroupNetworkManagerConnectionsClient with the specified values.
//   - credential - used to authorize requests. Usually a credential from azidentity.
//   - options - pass nil to accept the default values.
func NewManagementGroupNetworkManagerConnectionsClient(credential azcore.TokenCredential, options *arm.ClientOptions) (*ManagementGroupNetworkManagerConnectionsClient, error) {
	cl, err := arm.NewClient(moduleName, moduleVersion, credential, options)
	if err != nil {
		return nil, err
	}
	client := &ManagementGroupNetworkManagerConnectionsClient{
		internal: cl,
	}
	return client, nil
}

// CreateOrUpdate - Create a network manager connection on this management group.
// If the operation fails it returns an *azcore.ResponseError type.
//
// Generated from API version 2024-07-01
//   - managementGroupID - The management group Id which uniquely identify the Microsoft Azure management group.
//   - networkManagerConnectionName - Name for the network manager connection.
//   - parameters - Network manager connection to be created/updated.
//   - options - ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateOptions contains the optional parameters for the
//     ManagementGroupNetworkManagerConnectionsClient.CreateOrUpdate method.
func (client *ManagementGroupNetworkManagerConnectionsClient) CreateOrUpdate(ctx context.Context, managementGroupID string, networkManagerConnectionName string, parameters ManagerConnection, options *ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateOptions) (ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateResponse, error) {
	var err error
	const operationName = "ManagementGroupNetworkManagerConnectionsClient.CreateOrUpdate"
	ctx = context.WithValue(ctx, runtime.CtxAPINameKey{}, operationName)
	ctx, endSpan := runtime.StartSpan(ctx, operationName, client.internal.Tracer(), nil)
	defer func() { endSpan(err) }()
	req, err := client.createOrUpdateCreateRequest(ctx, managementGroupID, networkManagerConnectionName, parameters, options)
	if err != nil {
		return ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateResponse{}, err
	}
	httpResp, err := client.internal.Pipeline().Do(req)
	if err != nil {
		return ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateResponse{}, err
	}
	if !runtime.HasStatusCode(httpResp, http.StatusOK, http.StatusCreated) {
		err = runtime.NewResponseError(httpResp)
		return ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateResponse{}, err
	}
	resp, err := client.createOrUpdateHandleResponse(httpResp)
	return resp, err
}

// createOrUpdateCreateRequest creates the CreateOrUpdate request.
func (client *ManagementGroupNetworkManagerConnectionsClient) createOrUpdateCreateRequest(ctx context.Context, managementGroupID string, networkManagerConnectionName string, parameters ManagerConnection, _ *ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateOptions) (*policy.Request, error) {
	urlPath := "/providers/Microsoft.Management/managementGroups/{managementGroupId}/providers/Microsoft.Network/networkManagerConnections/{networkManagerConnectionName}"
	if managementGroupID == "" {
		return nil, errors.New("parameter managementGroupID cannot be empty")
	}
	urlPath = strings.ReplaceAll(urlPath, "{managementGroupId}", url.PathEscape(managementGroupID))
	if networkManagerConnectionName == "" {
		return nil, errors.New("parameter networkManagerConnectionName cannot be empty")
	}
	urlPath = strings.ReplaceAll(urlPath, "{networkManagerConnectionName}", url.PathEscape(networkManagerConnectionName))
	req, err := runtime.NewRequest(ctx, http.MethodPut, runtime.JoinPaths(client.internal.Endpoint(), urlPath))
	if err != nil {
		return nil, err
	}
	reqQP := req.Raw().URL.Query()
	reqQP.Set("api-version", "2024-07-01")
	req.Raw().URL.RawQuery = reqQP.Encode()
	req.Raw().Header["Accept"] = []string{"application/json"}
	if err := runtime.MarshalAsJSON(req, parameters); err != nil {
		return nil, err
	}
	return req, nil
}

// createOrUpdateHandleResponse handles the CreateOrUpdate response.
func (client *ManagementGroupNetworkManagerConnectionsClient) createOrUpdateHandleResponse(resp *http.Response) (ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateResponse, error) {
	result := ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateResponse{}
	if err := runtime.UnmarshalAsJSON(resp, &result.ManagerConnection); err != nil {
		return ManagementGroupNetworkManagerConnectionsClientCreateOrUpdateResponse{}, err
	}
	return result, nil
}

// Delete - Delete specified pending connection created by this management group.
// If the operation fails it returns an *azcore.ResponseError type.
//
// Generated from API version 2024-07-01
//   - managementGroupID - The management group Id which uniquely identify the Microsoft Azure management group.
//   - networkManagerConnectionName - Name for the network manager connection.
//   - options - ManagementGroupNetworkManagerConnectionsClientDeleteOptions contains the optional parameters for the ManagementGroupNetworkManagerConnectionsClient.Delete
//     method.
func (client *ManagementGroupNetworkManagerConnectionsClient) Delete(ctx context.Context, managementGroupID string, networkManagerConnectionName string, options *ManagementGroupNetworkManagerConnectionsClientDeleteOptions) (ManagementGroupNetworkManagerConnectionsClientDeleteResponse, error) {
	var err error
	const operationName = "ManagementGroupNetworkManagerConnectionsClient.Delete"
	ctx = context.WithValue(ctx, runtime.CtxAPINameKey{}, operationName)
	ctx, endSpan := runtime.StartSpan(ctx, operationName, client.internal.Tracer(), nil)
	defer func() { endSpan(err) }()
	req, err := client.deleteCreateRequest(ctx, managementGroupID, networkManagerConnectionName, options)
	if err != nil {
		return ManagementGroupNetworkManagerConnectionsClientDeleteResponse{}, err
	}
	httpResp, err := client.internal.Pipeline().Do(req)
	if err != nil {
		return ManagementGroupNetworkManagerConnectionsClientDeleteResponse{}, err
	}
	if !runtime.HasStatusCode(httpResp, http.StatusOK, http.StatusNoContent) {
		err = runtime.NewResponseError(httpResp)
		return ManagementGroupNetworkManagerConnectionsClientDeleteResponse{}, err
	}
	return ManagementGroupNetworkManagerConnectionsClientDeleteResponse{}, nil
}

// deleteCreateRequest creates the Delete request.
func (client *ManagementGroupNetworkManagerConnectionsClient) deleteCreateRequest(ctx context.Context, managementGroupID string, networkManagerConnectionName string, _ *ManagementGroupNetworkManagerConnectionsClientDeleteOptions) (*policy.Request, error) {
	urlPath := "/providers/Microsoft.Management/managementGroups/{managementGroupId}/providers/Microsoft.Network/networkManagerConnections/{networkManagerConnectionName}"
	if managementGroupID == "" {
		return nil, errors.New("parameter managementGroupID cannot be empty")
	}
	urlPath = strings.ReplaceAll(urlPath, "{managementGroupId}", url.PathEscape(managementGroupID))
	if networkManagerConnectionName == "" {
		return nil, errors.New("parameter networkManagerConnectionName cannot be empty")
	}
	urlPath = strings.ReplaceAll(urlPath, "{networkManagerConnectionName}", url.PathEscape(networkManagerConnectionName))
	req, err := runtime.NewRequest(ctx, http.MethodDelete, runtime.JoinPaths(client.internal.Endpoint(), urlPath))
	if err != nil {
		return nil, err
	}
	reqQP := req.Raw().URL.Query()
	reqQP.Set("api-version", "2024-07-01")
	req.Raw().URL.RawQuery = reqQP.Encode()
	req.Raw().Header["Accept"] = []string{"application/json"}
	return req, nil
}

// Get - Get a specified connection created by this management group.
// If the operation fails it returns an *azcore.ResponseError type.
//
// Generated from API version 2024-07-01
//   - managementGroupID - The management group Id which uniquely identify the Microsoft Azure management group.
//   - networkManagerConnectionName - Name for the network manager connection.
//   - options - ManagementGroupNetworkManagerConnectionsClientGetOptions contains the optional parameters for the ManagementGroupNetworkManagerConnectionsClient.Get
//     method.
func (client *ManagementGroupNetworkManagerConnectionsClient) Get(ctx context.Context, managementGroupID string, networkManagerConnectionName string, options *ManagementGroupNetworkManagerConnectionsClientGetOptions) (ManagementGroupNetworkManagerConnectionsClientGetResponse, error) {
	var err error
	const operationName = "ManagementGroupNetworkManagerConnectionsClient.Get"
	ctx = context.WithValue(ctx, runtime.CtxAPINameKey{}, operationName)
	ctx, endSpan := runtime.StartSpan(ctx, operationName, client.internal.Tracer(), nil)
	defer func() { endSpan(err) }()
	req, err := client.getCreateRequest(ctx, managementGroupID, networkManagerConnectionName, options)
	if err != nil {
		return ManagementGroupNetworkManagerConnectionsClientGetResponse{}, err
	}
	httpResp, err := client.internal.Pipeline().Do(req)
	if err != nil {
		return ManagementGroupNetworkManagerConnectionsClientGetResponse{}, err
	}
	if !runtime.HasStatusCode(httpResp, http.StatusOK) {
		err = runtime.NewResponseError(httpResp)
		return ManagementGroupNetworkManagerConnectionsClientGetResponse{}, err
	}
	resp, err := client.getHandleResponse(httpResp)
	return resp, err
}

// getCreateRequest creates the Get request.
func (client *ManagementGroupNetworkManagerConnectionsClient) getCreateRequest(ctx context.Context, managementGroupID string, networkManagerConnectionName string, _ *ManagementGroupNetworkManagerConnectionsClientGetOptions) (*policy.Request, error) {
	urlPath := "/providers/Microsoft.Management/managementGroups/{managementGroupId}/providers/Microsoft.Network/networkManagerConnections/{networkManagerConnectionName}"
	if managementGroupID == "" {
		return nil, errors.New("parameter managementGroupID cannot be empty")
	}
	urlPath = strings.ReplaceAll(urlPath, "{managementGroupId}", url.PathEscape(managementGroupID))
	if networkManagerConnectionName == "" {
		return nil, errors.New("parameter networkManagerConnectionName cannot be empty")
	}
	urlPath = strings.ReplaceAll(urlPath, "{networkManagerConnectionName}", url.PathEscape(networkManagerConnectionName))
	req, err := runtime.NewRequest(ctx, http.MethodGet, runtime.JoinPaths(client.internal.Endpoint(), urlPath))
	if err != nil {
		return nil, err
	}
	reqQP := req.Raw().URL.Query()
	reqQP.Set("api-version", "2024-07-01")
	req.Raw().URL.RawQuery = reqQP.Encode()
	req.Raw().Header["Accept"] = []string{"application/json"}
	return req, nil
}

// getHandleResponse handles the Get response.
func (client *ManagementGroupNetworkManagerConnectionsClient) getHandleResponse(resp *http.Response) (ManagementGroupNetworkManagerConnectionsClientGetResponse, error) {
	result := ManagementGroupNetworkManagerConnectionsClientGetResponse{}
	if err := runtime.UnmarshalAsJSON(resp, &result.ManagerConnection); err != nil {
		return ManagementGroupNetworkManagerConnectionsClientGetResponse{}, err
	}
	return result, nil
}

// NewListPager - List all network manager connections created by this management group.
//
// Generated from API version 2024-07-01
//   - managementGroupID - The management group Id which uniquely identify the Microsoft Azure management group.
//   - options - ManagementGroupNetworkManagerConnectionsClientListOptions contains the optional parameters for the ManagementGroupNetworkManagerConnectionsClient.NewListPager
//     method.
func (client *ManagementGroupNetworkManagerConnectionsClient) NewListPager(managementGroupID string, options *ManagementGroupNetworkManagerConnectionsClientListOptions) *runtime.Pager[ManagementGroupNetworkManagerConnectionsClientListResponse] {
	return runtime.NewPager(runtime.PagingHandler[ManagementGroupNetworkManagerConnectionsClientListResponse]{
		More: func(page ManagementGroupNetworkManagerConnectionsClientListResponse) bool {
			return page.NextLink != nil && len(*page.NextLink) > 0
		},
		Fetcher: func(ctx context.Context, page *ManagementGroupNetworkManagerConnectionsClientListResponse) (ManagementGroupNetworkManagerConnectionsClientListResponse, error) {
			ctx = context.WithValue(ctx, runtime.CtxAPINameKey{}, "ManagementGroupNetworkManagerConnectionsClient.NewListPager")
			nextLink := ""
			if page != nil {
				nextLink = *page.NextLink
			}
			resp, err := runtime.FetcherForNextLink(ctx, client.internal.Pipeline(), nextLink, func(ctx context.Context) (*policy.Request, error) {
				return client.listCreateRequest(ctx, managementGroupID, options)
			}, nil)
			if err != nil {
				return ManagementGroupNetworkManagerConnectionsClientListResponse{}, err
			}
			return client.listHandleResponse(resp)
		},
		Tracer: client.internal.Tracer(),
	})
}

// listCreateRequest creates the List request.
func (client *ManagementGroupNetworkManagerConnectionsClient) listCreateRequest(ctx context.Context, managementGroupID string, options *ManagementGroupNetworkManagerConnectionsClientListOptions) (*policy.Request, error) {
	urlPath := "/providers/Microsoft.Management/managementGroups/{managementGroupId}/providers/Microsoft.Network/networkManagerConnections"
	if managementGroupID == "" {
		return nil, errors.New("parameter managementGroupID cannot be empty")
	}
	urlPath = strings.ReplaceAll(urlPath, "{managementGroupId}", url.PathEscape(managementGroupID))
	req, err := runtime.NewRequest(ctx, http.MethodGet, runtime.JoinPaths(client.internal.Endpoint(), urlPath))
	if err != nil {
		return nil, err
	}
	reqQP := req.Raw().URL.Query()
	if options != nil && options.SkipToken != nil {
		reqQP.Set("$skipToken", *options.SkipToken)
	}
	if options != nil && options.Top != nil {
		reqQP.Set("$top", strconv.FormatInt(int64(*options.Top), 10))
	}
	reqQP.Set("api-version", "2024-07-01")
	req.Raw().URL.RawQuery = reqQP.Encode()
	req.Raw().Header["Accept"] = []string{"application/json"}
	return req, nil
}

// listHandleResponse handles the List response.
func (client *ManagementGroupNetworkManagerConnectionsClient) listHandleResponse(resp *http.Response) (ManagementGroupNetworkManagerConnectionsClientListResponse, error) {
	result := ManagementGroupNetworkManagerConnectionsClientListResponse{}
	if err := runtime.UnmarshalAsJSON(resp, &result.ManagerConnectionListResult); err != nil {
		return ManagementGroupNetworkManagerConnectionsClientListResponse{}, err
	}
	return result, nil
}
