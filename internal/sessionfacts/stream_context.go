package sessionfacts

import (
	"context"
	"reflect"
)

type cancelledComputation struct{ err error }

func (c *computation) pulse() {
	if c.stream == nil {
		return
	}
	c.stream.work++
	if c.stream.work%1024 != 0 {
		return
	}
	if err := c.stream.ctx.Err(); err != nil {
		panic(cancelledComputation{err})
	}
	if c.stream.progress != nil {
		c.stream.progress(Progress{c.stream.phase, c.stream.work})
	}
	if err := c.stream.ctx.Err(); err != nil {
		panic(cancelledComputation{err})
	}
}
func (a *FactAccumulator) recoverCancellation(err *error) {
	if r := recover(); r != nil {
		if cancelled, ok := r.(cancelledComputation); ok {
			*err = cancelled.err
			a.abort()
		} else {
			panic(r)
		}
	}
}
func cloneMetadata(in Input) Input {
	return cloneMetadataValue(reflect.ValueOf(in)).Interface().(Input)
}
func cloneMetadataValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		r := reflect.New(v.Type().Elem())
		r.Elem().Set(cloneMetadataValue(v.Elem()))
		return r
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		r := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			r.Index(i).Set(cloneMetadataValue(v.Index(i)))
		}
		return r
	case reflect.Struct:
		r := reflect.New(v.Type()).Elem()
		r.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" {
				r.Field(i).Set(cloneMetadataValue(v.Field(i)))
			}
		}
		return r
	default:
		return v
	}
}
func (a *FactAccumulator) context(ctx context.Context, phase string) { a.ctx = ctx; a.phase = phase }
