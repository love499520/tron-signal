# Tron Signal API 文档

> 仅允许管理登录态访问（/docs）

## 鉴权模型
系统仅区分【内网白名单 / 外网访问】，不区分 HTTP / WS  
- 内网 IP 白名单：免 Token  
- 外网访问：必须携带 Token  
- Token 传递方式（任选其一）：  
  - Authorization: Bearer <TOKEN>  
  - X-Token: <TOKEN>  
  - ?token=<TOKEN>  

## 基础状态

### GET /api/status
返回系统运行状态

{
  Listening,
  LastHeight,
  LastHash,
  LastTimeISO,
  Reconnects,
  ConnectedKeys,
  JudgeRule,
  Machines
}

## 判定规则切换

### POST /api/judge/switch
参数：
- rule: lucky / big / odd
- confirm: true / false

规则：
- 必须二次确认
- 切换会自动停止所有状态机
- 清空计数器 / 运行态 / 去重缓存

## 状态机

### GET /api/machines
获取所有状态机配置与运行态

### PUT /api/machines
整表保存状态机配置

规则：
- 支持多个状态机
- HIT 规则仅在基础触发后执行一次
- HIT 关闭时不做任何判断
- offset 表示 T+X 中的 X

## 数据源

### GET /api/sources
### POST /api/sources/upsert
### POST /api/sources/delete

数据源规则：
- 仅 HTTP（REST / JSON-RPC）
- 不区分主源 / 兜底源
- 采用“先到先用”
- 每个源包含：
  - 基础轮询阈值
  - 上限频率阈值

## Token 管理（仅管理登录态）

### GET /api/tokens
### POST /api/tokens
### DELETE /api/tokens

## 白名单

### GET /api/whitelist
### PUT /api/whitelist

## 日志

### GET /api/logs
参数：
- q
- major=1（仅重大事故）

重大事故包括：
- ABNORMAL_RESTART
- 区块高度跳跃
- 区块丢失
- 数据源不可用
- 轮询连续失败

## 实时状态（SSE）

### GET /sse/status
- 仅用于 UI 状态更新
- 不用于信号广播

## WebSocket

### GET /ws
- 仅用于信号广播
- 不参与状态计算
- 不保证确认或重发

## 声明
- Tron 链不支持区块 WS
- 本系统仅使用 HTTP 轮询
- 未全部验收前不得接入真实交易
