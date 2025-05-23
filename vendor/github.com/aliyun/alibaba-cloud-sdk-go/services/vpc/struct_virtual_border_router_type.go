package vpc

//Licensed under the Apache License, Version 2.0 (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at
//
//http://www.apache.org/licenses/LICENSE-2.0
//
//Unless required by applicable law or agreed to in writing, software
//distributed under the License is distributed on an "AS IS" BASIS,
//WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//See the License for the specific language governing permissions and
//limitations under the License.
//
// Code generated by Alibaba Cloud SDK Code Generator.
// Changes may cause incorrect behavior and will be lost if the code is regenerated.

// VirtualBorderRouterType is a nested struct in vpc response
type VirtualBorderRouterType struct {
	CreationTime                     string                                       `json:"CreationTime" xml:"CreationTime"`
	Status                           string                                       `json:"Status" xml:"Status"`
	Type                             string                                       `json:"Type" xml:"Type"`
	MinTxInterval                    int64                                        `json:"MinTxInterval" xml:"MinTxInterval"`
	PeerIpv6GatewayIp                string                                       `json:"PeerIpv6GatewayIp" xml:"PeerIpv6GatewayIp"`
	PConnVbrExpireTime               string                                       `json:"PConnVbrExpireTime" xml:"PConnVbrExpireTime"`
	PhysicalConnectionOwnerUid       string                                       `json:"PhysicalConnectionOwnerUid" xml:"PhysicalConnectionOwnerUid"`
	ActivationTime                   string                                       `json:"ActivationTime" xml:"ActivationTime"`
	PhysicalConnectionBusinessStatus string                                       `json:"PhysicalConnectionBusinessStatus" xml:"PhysicalConnectionBusinessStatus"`
	Description                      string                                       `json:"Description" xml:"Description"`
	TerminationTime                  string                                       `json:"TerminationTime" xml:"TerminationTime"`
	MinRxInterval                    int64                                        `json:"MinRxInterval" xml:"MinRxInterval"`
	PeerGatewayIp                    string                                       `json:"PeerGatewayIp" xml:"PeerGatewayIp"`
	Name                             string                                       `json:"Name" xml:"Name"`
	VbrId                            string                                       `json:"VbrId" xml:"VbrId"`
	VlanId                           int                                          `json:"VlanId" xml:"VlanId"`
	VlanInterfaceId                  string                                       `json:"VlanInterfaceId" xml:"VlanInterfaceId"`
	CircuitCode                      string                                       `json:"CircuitCode" xml:"CircuitCode"`
	LocalIpv6GatewayIp               string                                       `json:"LocalIpv6GatewayIp" xml:"LocalIpv6GatewayIp"`
	LocalGatewayIp                   string                                       `json:"LocalGatewayIp" xml:"LocalGatewayIp"`
	PeeringSubnetMask                string                                       `json:"PeeringSubnetMask" xml:"PeeringSubnetMask"`
	EnableIpv6                       bool                                         `json:"EnableIpv6" xml:"EnableIpv6"`
	RouteTableId                     string                                       `json:"RouteTableId" xml:"RouteTableId"`
	DetectMultiplier                 int64                                        `json:"DetectMultiplier" xml:"DetectMultiplier"`
	EccId                            string                                       `json:"EccId" xml:"EccId"`
	CloudBoxInstanceId               string                                       `json:"CloudBoxInstanceId" xml:"CloudBoxInstanceId"`
	RecoveryTime                     string                                       `json:"RecoveryTime" xml:"RecoveryTime"`
	PhysicalConnectionStatus         string                                       `json:"PhysicalConnectionStatus" xml:"PhysicalConnectionStatus"`
	PeeringIpv6SubnetMask            string                                       `json:"PeeringIpv6SubnetMask" xml:"PeeringIpv6SubnetMask"`
	AccessPointId                    string                                       `json:"AccessPointId" xml:"AccessPointId"`
	PConnVbrChargeType               string                                       `json:"PConnVbrChargeType" xml:"PConnVbrChargeType"`
	PhysicalConnectionId             string                                       `json:"PhysicalConnectionId" xml:"PhysicalConnectionId"`
	Bandwidth                        int                                          `json:"Bandwidth" xml:"Bandwidth"`
	ResourceGroupId                  string                                       `json:"ResourceGroupId" xml:"ResourceGroupId"`
	EcrId                            string                                       `json:"EcrId" xml:"EcrId"`
	SitelinkEnable                   bool                                         `json:"SitelinkEnable" xml:"SitelinkEnable"`
	EcrAttatchStatus                 string                                       `json:"EcrAttatchStatus" xml:"EcrAttatchStatus"`
	EcrOwnerId                       string                                       `json:"EcrOwnerId" xml:"EcrOwnerId"`
	AssociatedPhysicalConnections    AssociatedPhysicalConnections                `json:"AssociatedPhysicalConnections" xml:"AssociatedPhysicalConnections"`
	AssociatedCens                   AssociatedCensInDescribeVirtualBorderRouters `json:"AssociatedCens" xml:"AssociatedCens"`
	Tags                             TagsInDescribeVirtualBorderRouters           `json:"Tags" xml:"Tags"`
}
