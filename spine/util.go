package spine

import (
	"errors"
	"reflect"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
)

var notFoundError = errors.New("data not found")

func dataCopyOfType[T any](rdata any) (T, error) {
	x := any(*new(T))

	if rdata == nil {
		return x.(T), notFoundError
	}

	v := reflect.ValueOf(rdata)
	kind := v.Kind()
	if kind == reflect.Ptr && v.IsNil() {
		return x.(T), notFoundError
	}

	data, ok := rdata.(T)
	if !ok {
		return x.(T), notFoundError
	}

	return data, nil
}

// Note: the type has to be a pointer!
func LocalFeatureDataCopyOfType[T any](feature api.FeatureLocalInterface, function model.FunctionType) (T, error) {
	return dataCopyOfType[T](feature.DataCopy(function))
}

// Note: the type has to be a pointer!
func RemoteFeatureDataCopyOfType[T any](remote api.FeatureRemoteInterface, function model.FunctionType) (T, error) {
	return dataCopyOfType[T](remote.DataCopy(function))
}
