package gormTracing

import (
	"fmt"
	"github.com/opentracing/opentracing-go"
	tracerLog "github.com/opentracing/opentracing-go/log"
	"gorm.io/gorm"
)

const (
	gormSpanKey        = "__gorm_span"
	callBackBeforeName = "opentracing:before"
	callBackAfterName  = "opentracing:after"
)

func before(db *gorm.DB) {
	// 先从父级spans生成子span
	span, _ := opentracing.StartSpanFromContext(db.Statement.Context, "gorm")
	// 利用db实例去
	db.InstanceSet(gormSpanKey, span)
	return
}

func after(db *gorm.DB) {
	_span, isExist := db.InstanceGet(gormSpanKey)
	if !isExist {
		return
	}
	span, ok := _span.(opentracing.Span)
	if !ok {
		return
	}
	defer func() {
		db.Statement.Settings.Delete(fmt.Sprintf("%p", db.Statement) + gormSpanKey)
	}()
	defer span.Finish()

	// Error
	if db.Error != nil {
		span.LogFields(tracerLog.Error(db.Error))
	}

	// sql
	span.LogFields(tracerLog.String("sql", db.Dialector.Explain(db.Statement.SQL.String(), db.Statement.Vars...)))
	return
}

func register(db *gorm.DB) {
	db.Callback().Create().Before("gorm:before_create").Register(callBackBeforeName, before)
	db.Callback().Query().Before("gorm:query").Register(callBackBeforeName, before)
	db.Callback().Delete().Before("gorm:before_delete").Register(callBackBeforeName, before)
	db.Callback().Update().Before("gorm:setup_reflect_value").Register(callBackBeforeName, before)
	db.Callback().Row().Before("gorm:row").Register(callBackBeforeName, before)
	db.Callback().Raw().Before("gorm:raw").Register(callBackBeforeName, before)

	db.Callback().Create().After("gorm:after_create").Register(callBackBeforeName, after)
	db.Callback().Query().After("gorm:after_query").Register(callBackBeforeName, after)
	db.Callback().Delete().After("gorm:after_delete").Register(callBackBeforeName, after)
	db.Callback().Update().After("gorm:after_update").Register(callBackBeforeName, after)
	db.Callback().Row().After("gorm:row").Register(callBackBeforeName, after)
	db.Callback().Raw().After("gorm:raw").Register(callBackBeforeName, after)
}
