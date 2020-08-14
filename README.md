# Golang 上手GORM V2 + Opentracing链路追踪优化CRUD体验（源码阅读）

## 一、前言

 ![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020742_5c236a38_2051718.png "2.png")
![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020749_f61e6f4b_2051718.png "1.png")

> 系统环境（过几年我翻回来看或许会感慨我当初如此不堪）  
> go version go1.14.3 windows/amd64  
> gorm.io/gorm v0.2.31

[我的Blog: Avtion](https://www.avtion.cn/)

不会吧不会吧，0202年了，还有人单纯依赖日志去排查CRUD问题？快了解一下链路追踪吧！

- [GORM v2 技术文档](https://v2.gorm.io/zh_CN/)  
- [JaegerOpentracing 技术文档](https://www.jaegertracing.io/docs/1.18/getting-started/)

六月份前后，比较有名的`GORM框架`更新了V2版本，尽管现在依旧在测试阶段，但是我们还是能体验一下框架的一部分新特性 Feature，其中最馋的还是支持`Context`上下文传递的特性，结合分布式链路追踪技术，有助于我们服务在分布式部署的情况下精准排查问题。

还是要提及一下，ORM作为辅助工具能帮助我们快速构建项目，追求极致的响应速度应该手撸SQL，但是手撸SQL往往会遇到SQL注入、过程繁琐的问题，所以ORM是一把双刃剑，利用反射牺牲一定的性能以便我们更快上手项目。

文章按例依旧有部分源码分析，之前有做过同类型ORM框架`XORM`的链路追踪教程，有兴趣的可以看一下

[Golang XORM实现分布式链路追踪（源码分析，分布式CRUD必学）](https://www.avtion.cn/post/10012/)

## 二、Docker搭建Opentracing + jaeger all in one平台

注：Docker是最简单的，还有其他的方式，有兴趣的朋友可以去翻阅技术文档

**使用的镜像：`jaegertracing/all-in-one:1.18`**

Docker命令
```
docker run -d --name jaeger -e COLLECTOR_ZIPKIN_HTTP_PORT=9411 -p 5775:5775/udp -p 6831:6831/udp -p 6832:6832/udp -p 5778:5778 -p 16686:16686 -p 14268:14268 -p 14250:14250 -p 9411:9411 jaegertracing/all-in-one:1.18
```

浏览器访问`localhost:16686`，可以看到`JaegerUI`界面，如下所示：

![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020802_6ae8e39c_2051718.png "3.png")


## 三、创建项目

在项目目录下使用控制台输入，~~懂得都懂~~

```
go mod init
go get -u gorm.io/gorm
go get -u github.com/uber/jaeger-client-go
```

## 四、编写CallBacks插件

这里的CallBacks和模型的钩子不一样，CallBacks伴随GORM的DB对象整个生命周期，我们需要利用CallBacks对GORM框架进行侵入，以达到操作和访问GORM的DB对象的行为

### 1. 在每次SQL操作前从context上下文生成子span

通常我们服务（业务）在入口会分配一个根Span，然后在后续操作中会分裂出子Span，每个span都有自己的具体的标识，Finsh之后就会汇集在我们的链路追踪系统中

![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020859_7a5f4845_2051718.png "4.png")
（OpenTracing的Span示意图，我初学也看不懂，其实也就那样）

> 文件：gormTracing.go

```go
package gormTracing

// 包内静态变量
const gormSpanKey = "__gorm_span"

func before(db *gorm.DB) {
	// 先从父级spans生成子span ---> 这里命名为gorm，但实际上可以自定义
	// 自己喜欢的operationName
	span, _ := opentracing.StartSpanFromContext(db.Statement.Context, "gorm")
	
	// 利用db实例去传递span
	db.InstanceSet(gormSpanKey, span)
	
	return
}
```
就这么朴实无华的两行代码就能生成子span，按照惯例，我们需要抛弃掉`StartSpanFromContext`第二个结果，因为我们不能把父span覆盖掉，同时利用db的Setting`（sync.Map）`去寄存子Span

下文会对`db.InstanceSet`方法进行源码说明，有兴趣的朋友可以看一下

## 2. 在每次SQL操作后从DB实例拿到Span并记录数据

> 文件: gormTracing.go

```go
// 注意，这里重命名了log模块
import tracerLog "github.com/opentracing/opentracing-go/log"

func after(db *gorm.DB) {
	// 从GORM的DB实例中取出span
	_span, isExist := db.InstanceGet(gormSpanKey)
	if !isExist {
	    // 不存在就直接抛弃掉
		return
	}

	// 断言进行类型转换
	span, ok := _span.(opentracing.Span)
	if !ok {
		return
	}
	// <---- 一定一定一定要Finsih掉！！！
	defer span.Finish()

	// Error
	if db.Error != nil {
		span.LogFields(tracerLog.Error(db.Error))
	}

	// sql --> 写法来源GORM V2的日志
	span.LogFields(tracerLog.String("sql", db.Dialector.Explain(db.Statement.SQL.String(), db.Statement.Vars...)))
	return
}
```

同样非常简单地就能从DB的Setting里面拿到用于处理GORM操作的子Span，我们只需要调用span的LogFields方法就能记录下我们想要的信息

## 3. 创建结构体，实现gorm.Plugin接口

> 文件: gormTracing.go

```go
const (
	callBackBeforeName = "opentracing:before"
	callBackAfterName  = "opentracing:after"
)

type OpentracingPlugin struct {}

func (op *OpentracingPlugin) Name() string {
	return "opentracingPlugin"
}

func (op *OpentracingPlugin) Initialize(db *gorm.DB) (err error) {
	// 开始前 - 并不是都用相同的方法，可以自己自定义
	db.Callback().Create().Before("gorm:before_create").Register(callBackBeforeName, before)
	db.Callback().Query().Before("gorm:query").Register(callBackBeforeName, before)
	db.Callback().Delete().Before("gorm:before_delete").Register(callBackBeforeName, before)
	db.Callback().Update().Before("gorm:setup_reflect_value").Register(callBackBeforeName, before)
	db.Callback().Row().Before("gorm:row").Register(callBackBeforeName, before)
	db.Callback().Raw().Before("gorm:raw").Register(callBackBeforeName, before)

	// 结束后 - 并不是都用相同的方法，可以自己自定义
	db.Callback().Create().After("gorm:after_create").Register(callBackAfterName, after)
	db.Callback().Query().After("gorm:after_query").Register(callBackAfterName, after)
	db.Callback().Delete().After("gorm:after_delete").Register(callBackAfterName, after)
	db.Callback().Update().After("gorm:after_update").Register(callBackAfterName, after)
	db.Callback().Row().After("gorm:row").Register(callBackAfterName, after)
	db.Callback().Raw().After("gorm:raw").Register(callBackAfterName, after)
	return
}


// 告诉编译器这个结构体实现了gorm.Plugin接口
var _ gorm.Plugin = &OpentracingPlugin{}
```

我们需要非常繁琐地给GORM所有的最终操作（源码分析会提到）注册上刚刚编写的两个方法，分别是每个最终操作的最开始和结尾，这里直接提一下GORM的Plugin接口，源码如下：
```go
// Plugin GORM plugin interface
type Plugin interface {
	Name() string
	Initialize(*DB) error
}
```
我们只需要实现这两个方法即可实现这个接口，非常简单。

## 五、单元测试

### 1. 初始化Jeager

> 文件: gormTracing_test.go

```go
package gormTracing

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
```

感兴趣的朋友一定一定要自己去阅读一下Jaeger的技术文档，这里只是范例，而不是标准，初始化Jeager一定是根据实际业务去实现，这里只是简单示范。

### 2. 实现GORM官方范例

> [GORM V2技术文档 - 快速入门](https://v2.gorm.io/zh_CN/docs/)

> 文件: gormTracing_test.go

```go
type Product struct {
	gorm.Model
	Code  string
	Price uint
}
```

定义一个非常简单的结构体作为模型

```go
// 很多人会不知道V2如何连接MySQL数据库
// 需要利用Driver来实现
import "gorm.io/driver/mysql"

func Test_GormTracing(t *testing.T) {
    // 1. 初始化Jaeger
	closer, err := initJaeger()
	if err != nil{
		t.Fatal(err)
	}
	defer closer.Close()

    // 2. 连接数据库
	dsn := "root:root@tcp(localhost:3306)/test?charset=utf8mb4&parseTime=True&loc=Local"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	
	// 3. 最重要的一步，使用我们定义的插件
	_ = db.Use(&OpentracingPlugin{})

	// 迁移 schema ---> 生成对应的数据表
	_ = db.AutoMigrate(&Product{})

	// 4. 生成新的Span - 注意将span结束掉，不然无法发送对应的结果
	span := opentracing.StartSpan("gormTracing unit test")
	defer span.Finish()

	// 5. 把生成的Root Span写入到Context上下文，获取一个子Context
	// 通常在Web项目中，Root Span由中间件生成
	ctx := opentracing.ContextWithSpan(context.Background(), span)
	
	// 6. 将上下文传入DB实例，生成Session会话
	// 这样子就能把这个会话的全部信息反馈给Jaeger
	session := db.WithContext(ctx)
	
	// ---> 下面就是GORM的范例

	// Create
	session.Create(&Product{Code: "D42", Price: 100})

	// Read
	var product Product
	session.First(&product, 1) // 根据整形主键查找
	session.First(&product, "code = ?", "D42") // 查找 code 字段值为 D42 的记录

	// Update - 将 product 的 price 更新为 200
	session.Model(&product).Update("Price", 200)
	// Update - 更新多个字段
	session.Model(&product).Updates(Product{Price: 200, Code: "F42"}) // 仅更新非零值字段
	session.Model(&product).Updates(map[string]interface{}{"Price": 200, "Code": "F42"})

	// Delete - 删除 product
	session.Delete(&product, 1)
}
```

### 3. 执行并查看结果

```go
=== RUN   Test_GormTracing
--- PASS: Test_GormTracing (0.42s)
PASS

Process finished with exit code 0
```

控制台显示程序正常运行，我们访问Jaeger控制台（`localhost:16686`），可以看到有一条新的记录

![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020816_a28baca5_2051718.png "5.png")

点击进入查看详情，可以非常清楚第看见我们整个单元测试从开始到结束的SQL执行情况，总共执行了7条SQL命令，整个过程耗时163.03ms（注意！这不代表GORM性能测试，因为我的数据库部署在树莓派，同时使用远程连接，查询过程会比较慢）

![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020823_0724fc06_2051718.png "6.png")

我们可以点开对应的Span，可以看到每次GORM操作所执行的SQL命令。

![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020830_f939c9cc_2051718.png "7.png")

至此，使用OpenTracing对GORM执行过程进行链路追踪已经成功实现，从此摆脱需要检索庞大的日志查找慢查询、异常和错误的情况，直接一目了然。

### 4. 杂耍 - 并发情况下链路追踪的效果

```go
func Test_GormTracing2(t *testing.T) {
	closer, err := initJaeger()
	if err != nil{
		t.Fatal(err)
	}
	defer closer.Close()

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Use(&OpentracingPlugin{})

	rand.Seed(time.Now().UnixNano())

	num,wg := 1<<10, &sync.WaitGroup{}

	wg.Add(num)

	for i :=0 ;i < num; i ++{
		go func(t int) {
			span := opentracing.StartSpan(fmt.Sprintf("gormTracing unit test %d", t))
			defer span.Finish()

			ctx := opentracing.ContextWithSpan(context.Background(), span)
			session := db.WithContext(ctx)

			p := &Product{Code: strconv.Itoa(t), Price: uint(rand.Intn(1<<10))}

			session.Create(p)

			session.First(p, p.ID)

			session.Delete(p, p.ID)

			wg.Done()
		}(i)
	}

	wg.Wait()
}
```

不出意外，直接把我的小树莓派打宕机了

```
2020/08/15 00:54:29 D:/GoPath/src/gormTracing/gormTracing_test.go:122 Error 1040: Too many connections
[224.000ms] [rows:0] INSERT INTO `products` (`created_at`,`updated_at`,`deleted_at`,`code`,`price`) VALUES ("2020-08-15 00:54:29.153","2020-08-15 00:54:29.153",NULL,"152",206)
[mysql] 2020/08/15 00:54:44 packets.go:36: read tcp 192.168.50.3:11966->113.119.123.000:10012: wsarecv: An existing connection was forcibly closed by the remote host.
[mysql] 2020/08/15 00:54:44 packets.go:36: read tcp 192.168.50.3:11972->113.119.123.000:10012: wsarecv: An existing connection was forcibly closed by the remote host.
```

找到其中一个倒霉蛋，可以看到链路追踪记录下发生的错误，插入操作长时间获取不到数据库连接发生阻塞的情况。

![输入图片说明](https://images.gitee.com/uploads/images/2020/0815/020842_c4e6b40c_2051718.png "8.png")

## 六、 GORM V2 部分源码阅读

### 1. DB和Session对象

> gorm.go

```go
// DB GORM DB definition
type DB struct {
    // 配置信息
	*Config
	
	// 错误
	Error        error
	
	// sql执行影响的行数
	RowsAffected int64
	
	// 不太好翻译，个人觉得应该叫状态，因为这个字段
	// 保存了GORM的DB现场，主要用来进行链式操作
	// ---> 非常重要哦
	Statement    *Statement
	
	// 克隆数，初始创建的DB为1，分裂创建Session之后Session是1
	// 如果会话为DEBUG模式则为2
	clone        int
}

// Session session config when create session with Session() method
type Session struct {
    // 如果为真，则只生成SQL语句而不执行
	DryRun                 bool
	// stmt预处理，如果为真则进行SQL预处理，避免SQL注入
	PrepareStmt            bool
	// 是否有条件，如果Session处在Debug模式则为真
	WithConditions         bool
	// 影响MySQL的事务操作
	SkipDefaultTransaction bool
	
	// 上下文 --> 能传递上下文就能实现很多骚操作
	Context                context.Context
	
	// 日志对象
	Logger                 logger.Interface
	// 创建当前时间戳的方法
	NowFunc                func() time.Time
}
```

比较有意思的是DB结构体的Clone字段，会影响DB链式操作

> gorm.go

```go
func (db *DB) getInstance() *DB {
	if db.clone > 0 {
		tx := &DB{Config: db.Config}

		if db.clone == 1 {
			// clone with new statement
			tx.Statement = &Statement{
				DB:       tx,
				ConnPool: db.Statement.ConnPool,
				Context:  db.Statement.Context,
				Clauses:  map[string]clause.Clause{},
			}
		} else {
			// with clone statement
			tx.Statement = db.Statement.clone()
			tx.Statement.DB = tx
		}

		return tx
	}

	return db
}
```

如果clone字段等于0，则直接返回db，在链式操作的过程需要不断堆叠WHERE、JOIN这类方法时候，但并不需要产生新的DB，可以直接返回DB

大于0的情况就有区别的，首先是最简单的，等于1的情况，会创建一个新的DB对象，复制MySQL连接对象，连接池，上下文，以及关联信息配置。

不等于1的情况，就是DEBUG模式，在DEBUG模式下，Clone为2，意味着需要调用`db.Statement.clone()`，将全部状态都拷贝过来，包括Settings这个并发Map
```go
// Debug start debug mode
func (db *DB) Debug() (tx *DB) {
	return db.Session(&Session{
		WithConditions: true,
		Logger:         db.Logger.LogMode(logger.Info),
	})
}

// Session create new db session
func (db *DB) Session(config *Session) *DB {
    ...

	if config.WithConditions {
		tx.clone = 2
	}
    ...
	return tx
}
```


### 2.Statment对象

该结构体基本上存储了所有链式操作需要保存状态的信息

```go
type Statement struct {
    // 指向当前持有Statement的DB
	*DB
	// Join的信息，就是`FROM A JOIN B WHERE A.id = B.id`
	TableExpr            *clause.Expr
	// 当前的表
	Table                string
	// 模型
	Model                interface{}
	// 常用于永久删除（跳过软删除）
	Unscoped             bool
	// 目标对象，就是我们Find或者Get操作时需要赋值的对象
	Dest                 interface{}
	// 反射值 ---> 只需要进行一次反射就能获取，优化了反射的时间
	ReflectValue         reflect.Value
	// 关联，高级查询用
	Clauses              map[string]clause.Clause
	// 结果是否去重
	Distinct             bool
	// SELECT的字段
	Selects              []string // selected columns
	Omits                []string // omit columns
	Joins                map[string][]interface{}
	Preloads             map[string][]interface{}
	// ---> 重点 Settings
	Settings             sync.Map
	ConnPool             ConnPool
	Schema               *schema.Schema
	// --> 重点上下文
	Context              context.Context
	RaiseErrorOnNotFound bool
	UpdatingColumn       bool
	// --> 重点 SQL语句
	SQL                  strings.Builder
	// --> 重点 SQL操作的参数
	Vars                 []interface{}
	// 如果目标对象为切片或者数组，这里记录了当前目标的下标
	CurDestIndex         int
	attrs                []interface{}
	assigns              []interface{}
}
```


### 3. Settings字段（Sync.Map）代替Context传递数据

```go
// Set store value with key into current db instance's context
func (db *DB) Set(key string, value interface{}) *DB {
	tx := db.getInstance()
	tx.Statement.Settings.Store(key, value)
	return tx
}

// Get get value with key from current db instance's context
func (db *DB) Get(key string) (interface{}, bool) {
	return db.Statement.Settings.Load(key)
}

// InstanceSet store value with key into current db instance's context
func (db *DB) InstanceSet(key string, value interface{}) *DB {
	tx := db.getInstance()
	tx.Statement.Settings.Store(fmt.Sprintf("%p", tx.Statement)+key, value)
	return tx
}

// InstanceGet get value with key from current db instance's context
func (db *DB) InstanceGet(key string) (interface{}, bool) {
	return db.Statement.Settings.Load(fmt.Sprintf("%p", db.Statement) + key)
}
```

首先我们已经知道`db.Statement.Settings`是一个`Sync.Map`（并发安全的Map），`Set`、`Get`方法与`InstanceSet`、`InstanceGet`最大的区别就是 ***加入了`db`的地址作为键值***，这样有助于我们保证上下文传递的数据唯一，同时实现了并发安全。


### 4. CallBacks插件的使用

作为侵入GORM框架的关键一环，插件的的使用不可获取，实现的逻辑其实非常简单，就是定义一个接口，我们实现了这个接口就能（~~肆意妄为~~）随心所欲操作关键的`DB`对象。

```go
// Plugin GORM plugin interface
type Plugin interface {
	Name() string
	Initialize(*DB) error
}


func (db *DB) Use(plugin Plugin) (err error) {
    // 获取一下插件的名
	name := plugin.Name()
	
	// 如果没有同名就初始化插件，如果有同名就直接报错
	if _, ok := db.Plugins[name]; !ok {
		if err = plugin.Initialize(db); err == nil {
			db.Plugins[name] = plugin
		}
	} else {
		return ErrRegistered
	}

	return err
}
```

谢谢阅读，知识无价，希望能帮到您。
