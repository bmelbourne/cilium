// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See License.txt in the project root for license information.
// Code generated by Microsoft (R) AutoRest Code Generator. DO NOT EDIT.
// Changes may cause incorrect behavior and will be lost if the code is regenerated.

package armnetwork

import "encoding/json"

// UnmarshalJSON implements the json.Unmarshaller interface for type AdminRulesClientCreateOrUpdateResponse.
func (a *AdminRulesClientCreateOrUpdateResponse) UnmarshalJSON(data []byte) error {
	res, err := unmarshalBaseAdminRuleClassification(data)
	if err != nil {
		return err
	}
	a.BaseAdminRuleClassification = res
	return nil
}

// UnmarshalJSON implements the json.Unmarshaller interface for type AdminRulesClientGetResponse.
func (a *AdminRulesClientGetResponse) UnmarshalJSON(data []byte) error {
	res, err := unmarshalBaseAdminRuleClassification(data)
	if err != nil {
		return err
	}
	a.BaseAdminRuleClassification = res
	return nil
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VPNConnectionsClientStartPacketCaptureResponse.
func (v *VPNConnectionsClientStartPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VPNConnectionsClientStopPacketCaptureResponse.
func (v *VPNConnectionsClientStopPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VPNGatewaysClientStartPacketCaptureResponse.
func (v *VPNGatewaysClientStartPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VPNGatewaysClientStopPacketCaptureResponse.
func (v *VPNGatewaysClientStopPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VPNLinkConnectionsClientGetIkeSasResponse.
func (v *VPNLinkConnectionsClientGetIkeSasResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualHubBgpConnectionsClientListAdvertisedRoutesResponse.
func (v *VirtualHubBgpConnectionsClientListAdvertisedRoutesResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualHubBgpConnectionsClientListLearnedRoutesResponse.
func (v *VirtualHubBgpConnectionsClientListLearnedRoutesResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewayConnectionsClientGetIkeSasResponse.
func (v *VirtualNetworkGatewayConnectionsClientGetIkeSasResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewayConnectionsClientStartPacketCaptureResponse.
func (v *VirtualNetworkGatewayConnectionsClientStartPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewayConnectionsClientStopPacketCaptureResponse.
func (v *VirtualNetworkGatewayConnectionsClientStopPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientGenerateVPNProfileResponse.
func (v *VirtualNetworkGatewaysClientGenerateVPNProfileResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientGeneratevpnclientpackageResponse.
func (v *VirtualNetworkGatewaysClientGeneratevpnclientpackageResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientGetFailoverAllTestDetailsResponse.
func (v *VirtualNetworkGatewaysClientGetFailoverAllTestDetailsResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.ExpressRouteFailoverTestDetailsArray)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientGetFailoverSingleTestDetailsResponse.
func (v *VirtualNetworkGatewaysClientGetFailoverSingleTestDetailsResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.ExpressRouteFailoverSingleTestDetailsArray)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientGetVPNProfilePackageURLResponse.
func (v *VirtualNetworkGatewaysClientGetVPNProfilePackageURLResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientStartExpressRouteSiteFailoverSimulationResponse.
func (v *VirtualNetworkGatewaysClientStartExpressRouteSiteFailoverSimulationResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientStartPacketCaptureResponse.
func (v *VirtualNetworkGatewaysClientStartPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientStopExpressRouteSiteFailoverSimulationResponse.
func (v *VirtualNetworkGatewaysClientStopExpressRouteSiteFailoverSimulationResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

// UnmarshalJSON implements the json.Unmarshaller interface for type VirtualNetworkGatewaysClientStopPacketCaptureResponse.
func (v *VirtualNetworkGatewaysClientStopPacketCaptureResponse) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}
