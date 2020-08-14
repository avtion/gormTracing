package gormTracing

import (
	"context"
	"fmt"
	"github.com/opentracing/opentracing-go"
	"github.com/uber/jaeger-client-go"
	"github.com/uber/jaeger-client-go/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"io"
	"math/rand"
	"strconv"
	"sync"
	"testing"
	"time"
)

const dsn = "root:root@tcp(localhost:10012)/test?charset=utf8mb4&parseTime=True&loc=Local"

func initJaeger() (closer io.Closer, err error) {
	// 根据配置初始化Tracer 返回Closer
	tracer, closer, err := (&config.Configuration{
		ServiceName: "gormTracing",
		Disabled:    false,
		Sampler: &config.SamplerConfig{
			Type: jaeger.SamplerTypeConst,
			// param的值在0到1之间，设置为1则将所有的Operation输出到Reporter
			Param: 1,
		},
		Reporter: &config.ReporterConfig{
			LogSpans:           true,
			LocalAgentHostPort: "localhost:6831",
		},
	}).NewTracer()
	if err != nil {
		return
	}

	// 设置全局Tracer - 如果不设置将会导致上下文无法生成正确的Span
	opentracing.SetGlobalTracer(tracer)
	return
}

type Product struct {
	gorm.Model
	Code  string
	Price uint
}

func Test_GormTracing(t *testing.T) {
	closer, err := initJaeger()
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Use(&OpentracingPlugin{})

	// 迁移 schema
	_ = db.AutoMigrate(&Product{})

	// 生成新的Span - 注意将span结束掉，不然无法发送对应的结果
	span := opentracing.StartSpan("gormTracing unit test")
	defer span.Finish()

	// 把生成的Root Span写入到Context上下文，获取一个子Context
	ctx := opentracing.ContextWithSpan(context.Background(), span)
	session := db.WithContext(ctx)

	// Create
	session.Create(&Product{Code: "D42", Price: 100})

	// Read
	var product Product
	session.First(&product, 1)                 // 根据整形主键查找
	session.First(&product, "code = ?", "D42") // 查找 code 字段值为 D42 的记录

	// Update - 将 product 的 price 更新为 200
	session.Model(&product).Update("Price", 200)
	// Update - 更新多个字段
	session.Model(&product).Updates(Product{Price: 200, Code: "F42"}) // 仅更新非零值字段
	session.Model(&product).Updates(map[string]interface{}{"Price": 200, "Code": "F42"})

	// Delete - 删除 product
	session.Delete(&product, 1)
}

func Test_GormTracing2(t *testing.T) {
	closer, err := initJaeger()
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Use(&OpentracingPlugin{})

	rand.Seed(time.Now().UnixNano())

	num, wg := 1<<10, &sync.WaitGroup{}

	wg.Add(num)

	for i := 0; i < num; i++ {
		go func(t int) {
			span := opentracing.StartSpan(fmt.Sprintf("gormTracing unit test %d", t))
			defer span.Finish()

			ctx := opentracing.ContextWithSpan(context.Background(), span)
			session := db.WithContext(ctx)

			p := &Product{Code: strconv.Itoa(t), Price: uint(rand.Intn(1 << 10))}

			session.Create(p)

			session.First(p, p.ID)

			session.Delete(p, p.ID)

			wg.Done()
		}(i)
	}

	wg.Wait()
}
