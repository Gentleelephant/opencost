# Allocation Filter Syntax

本文档记录 allocation 系列接口 `filter` 参数的语法。适用接口包括：

- `/allocation/autocomplete`
- `/allocation/compute`
- `/allocation/compute/summary`
- `/allocation/summary/topline`
- `/efficiency`
- `/efficiency/clusters`
- `/efficiency/clusters/summary`

带前缀的 canonical API 路径同样适用，例如：

- `/kapis/costwise.wiztelemetry.io/v1alpha1/allocation/compute`
- `/kapis/costwise.wiztelemetry.io/v1alpha1/efficiency/clusters/summary`

## 基本格式

```text
field<operator>"value"
```

示例：

```text
namespace:"kubecost"
cluster:"host"+namespace:"default"
label[app]:"cost-analyzer"
```

URL 查询参数中建议对特殊字符做 URL 编码，尤其是 `+`，避免被解释为空格：

```text
filter=cluster:%22host%22%2Bnamespace:%22default%22
```

## 操作符

| 语义 | 语法 | 示例 |
| --- | --- | --- |
| is / 等于 | `:` | `namespace:"kubecost"` |
| is not / 不等于 | `!:` | `namespace!:"kube-system"` |
| contains / 包含 | `~:` | `pod~:"prometheus"` |
| does not contain / 不包含 | `!~:` | `pod!~:"test"` |
| starts with / 前缀匹配 | `<~:` | `cluster<~:"prod-"` |
| does not start with / 非前缀匹配 | `!<~:` | `cluster!<~:"dev-"` |
| ends with / 后缀匹配 | `~>:` | `pod~>:"-server"` |
| does not end with / 非后缀匹配 | `!~>:` | `pod!~>:"-canary"` |

匹配是大小写敏感的。

## 逻辑组合

`+` 表示 AND：

```text
cluster:"host"+namespace:"default"
```

`|` 表示 OR：

```text
namespace:"kubecost"|namespace:"monitoring"
```

使用括号分组：

```text
(namespace:"kubecost"|namespace:"monitoring")+cluster<~:"prod-"
```

同一层级内不能混用 `+` 和 `|`。需要混用时必须用括号明确优先级：

```text
# 正确
namespace:"kubecost"+(cluster:"prod-a"|cluster:"prod-b")

# 错误
namespace:"kubecost"+cluster:"prod-a"|cluster:"prod-b"
```

## 多值

正向操作符的多值会按 OR 处理：

```text
namespace:"kubecost","monitoring"
```

等价于：

```text
namespace:"kubecost"|namespace:"monitoring"
```

否定操作符的多值会按 AND 处理：

```text
namespace!:"kube-system","default"
```

等价于：

```text
namespace!:"kube-system"+namespace!:"default"
```

## 支持字段

当前 allocation filter parser 支持以下字段：

| 字段 | 说明 | 示例 |
| --- | --- | --- |
| `cluster` | 集群 ID / 集群名 | `cluster:"host"` |
| `node` | 节点名 | `node:"node-1"` |
| `namespace` | 命名空间 | `namespace:"default"` |
| `controllerName` | Controller 名称 | `controllerName:"nginx"` |
| `controllerKind` | Controller 类型 | `controllerKind:"deployment"` |
| `container` | 容器名 | `container:"app"` |
| `pod` | Pod 名称 | `pod~:"prometheus"` |
| `provider` | Provider ID | `provider~:"aws"` |
| `services` | Service 列表 | `services~:"frontend"` |
| `label` | Allocation label map | `label[app]:"web"` |
| `annotation` | Allocation annotation map | `annotation[owner]:"platform"` |
| `department` | label/annotation alias | `department:"engineering"` |
| `environment` | label/annotation alias | `environment:"prod"` |
| `owner` | label/annotation alias | `owner:"team-a"` |
| `product` | label/annotation alias | `product:"costwise"` |
| `team` | label/annotation alias | `team:"platform"` |

注意：

- `account` 在 parser 中存在，但当前 allocation matcher 返回 `account property not implemented`，不要依赖它做过滤。
- `nodeLabel` 和 `namespaceLabel` 在 parser 中存在，但当前 allocation matcher 未实现对应 map 字段解析，allocation 系列接口中不要依赖它们。
- `controllerName` 匹配具体 controller 名称，`controllerKind` 匹配类型，例如 `deployment`、`statefulset`、`daemonset`。

## label 和 annotation

带 key 时匹配 map value：

```text
label[app]:"web"
annotation[owner]~:"platform"
```

不带 key 时匹配 map key 是否存在或 key 是否满足 contains/prefix/suffix：

```text
label~:"app"
annotation<~:"prometheus"
```

## services

`services` 是字符串数组字段。对 `services` 使用 `:` 或 `~:` 都会按包含某个 service 值处理：

```text
services:"frontend"
services~:"frontend"
services!~:"debug"
services<~:"api-"
services~>:"-internal"
```

## 常用示例

指定集群：

```text
cluster:"host"
```

指定集群和命名空间：

```text
cluster:"host"+namespace:"default"
```

排除系统命名空间：

```text
namespace!:"kube-system","default"
```

查询带指定 label 的 workload：

```text
label[app]:"web"
```

查询名称包含关键字的 Pod：

```text
pod~:"prometheus"
```

查询 prod 集群中的 deployment：

```text
cluster<~:"prod-"+controllerKind:"deployment"
```

