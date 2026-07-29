package model

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssue15VR940ModelJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		assert func(*testing.T, CmdType)
	}{
		{
			name: "SetpointDescriptionListData",
			input: `{
				"setpointDescriptionListData": {
					"setpointDescriptionData": [{
						"setpointId": 1,
						"measurementId": 11,
						"timeTableId": 21,
						"setpointType": "valueAbsolute",
						"unit": "degC",
						"scopeType": "roomAirTemperature",
						"label": "Room target temperature"
					}]
				}
			}`,
			assert: func(t *testing.T, cmd CmdType) {
				require.NotNil(t, cmd.SetpointDescriptionListData)
				require.Len(t, cmd.SetpointDescriptionListData.SetpointDescriptionData, 1)
			},
		},
		{
			name: "HvacOperationModeDescriptionListData",
			input: `{
				"hvacOperationModeDescriptionListData": {
					"hvacOperationModeDescriptionData": [{
						"operationModeId": 1,
						"operationModeType": "auto",
						"label": "Automatic"
					}]
				}
			}`,
			assert: func(t *testing.T, cmd CmdType) {
				require.NotNil(t, cmd.HvacOperationModeDescriptionListData)
				require.Len(t, cmd.HvacOperationModeDescriptionListData.HvacOperationModeDescriptionData, 1)
			},
		},
		{
			name: "HvacSystemFunctionDescriptionListData",
			input: `{
				"hvacSystemFunctionDescriptionListData": {
					"hvacSystemFunctionDescriptionData": [{
						"systemFunctionId": 1,
						"systemFunctionType": "heating",
						"label": "Heating circuit"
					}]
				}
			}`,
			assert: func(t *testing.T, cmd CmdType) {
				require.NotNil(t, cmd.HvacSystemFunctionDescriptionListData)
				require.Len(t, cmd.HvacSystemFunctionDescriptionListData.HvacSystemFunctionDescriptionData, 1)
			},
		},
		{
			name: "HvacSystemFunctionOperationModeRelationListData",
			input: `{
				"hvacSystemFunctionOperationModeRelationListData": {
					"hvacSystemFunctionOperationModeRelationData": [{
						"systemFunctionId": 1,
						"operationModeId": [1, 2, 3]
					}]
				}
			}`,
			assert: func(t *testing.T, cmd CmdType) {
				require.NotNil(t, cmd.HvacSystemFunctionOperationModeRelationListData)
				require.Len(t, cmd.HvacSystemFunctionOperationModeRelationListData.HvacSystemFunctionOperationModeRelationData, 1)
			},
		},
		{
			name: "HvacSystemFunctionSetpointRelationListData",
			input: `{
				"hvacSystemFunctionSetpointRelationListData": {
					"hvacSystemFunctionSetpointRelationData": [{
						"systemFunctionId": 1,
						"operationModeId": 2,
						"setpointId": [4, 5]
					}]
				}
			}`,
			assert: func(t *testing.T, cmd CmdType) {
				require.NotNil(t, cmd.HvacSystemFunctionSetPointRelationListData)
				require.Len(t, cmd.HvacSystemFunctionSetPointRelationListData.HvacSystemFunctionSetpointRelationData, 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cmd CmdType
			require.NoError(t, json.Unmarshal([]byte(test.input), &cmd))

			encoded, err := json.Marshal(cmd)
			require.NoError(t, err)
			assert.JSONEq(t, test.input, string(encoded))
			test.assert(t, cmd)
		})
	}
}

func TestIssue15SetpointDescriptionFieldTypes(t *testing.T) {
	tests := []struct {
		name  string
		owner reflect.Type
		field string
		want  reflect.Type
	}{
		{
			name:  "DataMeasurementId",
			owner: reflect.TypeOf(SetpointDescriptionDataType{}),
			field: "MeasurementId",
			want:  reflect.TypeOf((*MeasurementIdType)(nil)),
		},
		{
			name:  "DataTimeTableId",
			owner: reflect.TypeOf(SetpointDescriptionDataType{}),
			field: "TimeTableId",
			want:  reflect.TypeOf((*TimeTableIdType)(nil)),
		},
		{
			name:  "DataUnit",
			owner: reflect.TypeOf(SetpointDescriptionDataType{}),
			field: "Unit",
			want:  reflect.TypeOf((*UnitOfMeasurementType)(nil)),
		},
		{
			name:  "DataScopeType",
			owner: reflect.TypeOf(SetpointDescriptionDataType{}),
			field: "ScopeType",
			want:  reflect.TypeOf((*ScopeTypeType)(nil)),
		},
		{
			name:  "SelectorMeasurementId",
			owner: reflect.TypeOf(SetpointDescriptionListDataSelectorsType{}),
			field: "MeasurementId",
			want:  reflect.TypeOf((*MeasurementIdType)(nil)),
		},
		{
			name:  "SelectorTimeTableId",
			owner: reflect.TypeOf(SetpointDescriptionListDataSelectorsType{}),
			field: "TimeTableId",
			want:  reflect.TypeOf((*TimeTableIdType)(nil)),
		},
		{
			name:  "SelectorSetpointType",
			owner: reflect.TypeOf(SetpointDescriptionListDataSelectorsType{}),
			field: "SetpointType",
			want:  reflect.TypeOf((*SetpointTypeType)(nil)),
		},
		{
			name:  "SelectorScopeType",
			owner: reflect.TypeOf(SetpointDescriptionListDataSelectorsType{}),
			field: "ScopeType",
			want:  reflect.TypeOf((*ScopeTypeType)(nil)),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field, ok := test.owner.FieldByName(test.field)
			require.True(t, ok)
			assert.Equal(t, test.want.String(), field.Type.String())
		})
	}
}

func TestIssue15HvacFieldCardinalities(t *testing.T) {
	tests := []struct {
		name  string
		owner reflect.Type
		field string
		want  reflect.Type
	}{
		{
			name:  "SystemFunctionSelector",
			owner: reflect.TypeOf(HvacSystemFunctionListDataSelectorsType{}),
			field: "SystemFunctionId",
			want:  reflect.TypeOf((*HvacSystemFunctionIdType)(nil)),
		},
		{
			name:  "OperationModeRelationIds",
			owner: reflect.TypeOf(HvacSystemFunctionOperationModeRelationDataType{}),
			field: "OperationModeId",
			want:  reflect.TypeOf([]HvacOperationModeIdType{}),
		},
		{
			name:  "OperationModeRelationSelector",
			owner: reflect.TypeOf(HvacSystemFunctionOperationModeRelationListDataSelectorsType{}),
			field: "SystemFunctionId",
			want:  reflect.TypeOf((*HvacSystemFunctionIdType)(nil)),
		},
		{
			name:  "SetpointRelationIds",
			owner: reflect.TypeOf(HvacSystemFunctionSetpointRelationDataType{}),
			field: "SetpointId",
			want:  reflect.TypeOf([]SetpointIdType{}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field, ok := test.owner.FieldByName(test.field)
			require.True(t, ok)
			assert.Equal(t, test.want.String(), field.Type.String())
		})
	}
}
