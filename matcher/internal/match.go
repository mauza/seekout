package internal

import (
	"fmt"
	"log"
	"reflect"

	"github.com/mauza/seekout/lib"
)

type SeekerConditions map[string][]Condition

type Condition struct {
	Field  string `json:"field"`
	Op     string `json:"operator"`
	Value  any    `json:"value"`
	IsList bool   `json:"isList"`
}

var operators = map[string]func(any, any) bool{
	"=":  equals,
	">":  greaterThan,
	"<":  lessThan,
	">=": greaterThanEqual,
	"<=": lessThanEqual,
	"in": valueIn,
}

func CheckPropCondition(property lib.Property, condition Condition) bool {
	var opFunc func(any, any) bool
	if condition.IsList {
		opFunc = valueIn
	} else {
		opFunc = operators[condition.Op]
	}

	fieldValue, err := getFieldType(property, condition.Field)
	if err != nil {
		log.Printf("Error getting field value: %v", err)
		return false
	}

	conditionValue, err := cast(fieldValue, condition.Value)
	if err != nil {
		log.Printf("Error casting condition value: %v", err)
		return false
	}

	return opFunc(fieldValue, conditionValue)
}

func getFieldType(obj interface{}, fieldName string) (reflect.Type, error) {
	fmt.Println("field name: ", fieldName)
	value := reflect.ValueOf(obj)
	if value.Kind() == reflect.Ptr {
		value = value.Elem()
	}

	if value.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input object must be a struct or a pointer to a struct")
	}

	field, ok := value.Type().FieldByName(fieldName)
	if !ok {
		return nil, fmt.Errorf("field %s not found in struct %s", fieldName, value.Type().Name())
	}

	return field.Type, nil
}

func cast(value, target any) (any, error) {
	switch t := target.(type) {
	case string:
		return t, nil
	case int:
		return t, nil
	case float64:
		return t, nil
	case bool:
		return t, nil
	default:
		return nil, fmt.Errorf("unsupported type: %T", t)
	}
}

func valueIn(value, target any) bool {
	targetSlice, ok := target.([]any)
	if !ok {
		return false
	}
	for _, t := range targetSlice {
		if value == t {
			return true
		}
	}
	return false
}

func equals(value, target any) bool {
	return value == target
}

func greaterThan(value, target any) bool {
	switch t := target.(type) {
	case int:
		return value.(int) > t
	case float64:
		return value.(float64) > t
	case string:
		return value.(string) > t
	default:
		return false
	}
}

func lessThan(value, target any) bool {
	switch t := target.(type) {
	case int:
		return value.(int) < t
	case float64:
		return value.(float64) < t
	case string:
		return value.(string) < t
	default:
		return false
	}
}

func greaterThanEqual(value, target any) bool {
	switch t := target.(type) {
	case int:
		return value.(int) >= t
	case float64:
		return value.(float64) >= t
	case string:
		return value.(string) >= t
	default:
		return false
	}
}

func lessThanEqual(value, target any) bool {
	switch t := target.(type) {
	case int:
		return value.(int) <= t
	case float64:
		return value.(float64) <= t
	case string:
		return value.(string) <= t
	default:
		return false
	}
}
