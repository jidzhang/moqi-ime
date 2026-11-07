-- author: https://github.com/ChaosAlphard
-- 说明 https://github.com/gaboolic/rime-shuangpin-fuzhuma/pull/41
--
-- moqi 打包期补丁（2026-11-07，prebuild installCalculatorGuard 整文件替换）：
-- trigger 配置缺省为 'V' 且 init 自动把小写 'v' 加进前缀表，任何 v 开头、
-- 长度 >=3 的普通拼音输入（如 vuole/vhulaide）都会被当成算式送 load()，
-- 纯字母"算式"求值为 nil，Candidate(text=nil) 让 LuaTranslation 每次报
-- "bad argument #5 (string expected, got nil)"——产品里等于日常 v 开头
-- 输入每次会话都打 glog ERROR 文件。修法：算式必须含数字，纯字母直接
-- 返回；代价是 vpi 这类字母常量边角用法失效。另加 result~=nil 守卫兜底。
local M = {}

function M.init(env)
  local config = env.engine.schema.config
  env.name_space = env.name_space:gsub('^*', '')
  M.prefix = config:get_string(env.name_space .. '/trigger') or 'V'
  M.prefixes = { M.prefix }
  local lowerPrefix = M.prefix:lower()
  if lowerPrefix ~= M.prefix then
    table.insert(M.prefixes, lowerPrefix)
  end
end

local function startsWith(str, start)
  return string.sub(str, 1, string.len(start)) == start
end

local function truncateFromStart(str, truncateStr)
  return string.sub(str, string.len(truncateStr) + 1)
end

-- 函数表
local calcPlugin = {
  -- e, exp(1) = e^1 = e
  e = math.exp(1),
  -- π
  pi = math.pi
}

-- random([m [,n ]]) 返回m-n之间的随机数, n为空则返回1-m之间, 都为空则返回0-1之间的小数
local function random(...)
  return math.random(...)
end
-- 注册到函数表中
calcPlugin["rdm"] = random

-- 正弦
local function sin(x)
  return math.sin(x)
end
calcPlugin["sin"] = sin

-- 双曲正弦
local function sinh(x)
  return math.sinh(x)
end
calcPlugin["sinh"] = sinh

-- 反正弦
local function asin(x)
  return math.asin(x)
end
calcPlugin["asin"] = asin

-- 余弦
local function cos(x)
  return math.cos(x)
end
calcPlugin["cos"] = cos

-- 双曲余弦
local function cosh(x)
  return math.cosh(x)
end
calcPlugin["cosh"] = cosh

-- 反余弦
local function acos(x)
  return math.acos(x)
end
calcPlugin["acos"] = acos

-- 正切
local function tan(x)
  return math.tan(x)
end
calcPlugin["tan"] = tan

-- 双曲正切
local function tanh(x)
  return math.tanh(x)
end
calcPlugin["tanh"] = tanh

-- 反正切
local function atan(x)
  return math.atan(x)
end
calcPlugin["atan"] = atan

-- 返回以弧度为单位的角度。它与传统的 math.atan(y/x) 函数相比，有更好的数学定义
-- （例如x=0），见 lua 文档。
local function atan2(y, x)
  return math.atan2(y, x)
end
calcPlugin["atan2"] = atan2

-- 将角度从弧度转换为度 e.g. deg(π) = 180
local function deg(x)
  return math.deg(x)
end
calcPlugin["deg"] = deg

-- 将角度从度转换为弧度 e.g. rad(180) = π
local function rad(x)
  return math.rad(x)
end
calcPlugin["rad"] = rad

-- 返回两个值, 无法参与运算后续
-- 返回m,e 使得x = m*2^e
-- local function frexp(x)
--   return math.frexp(x)
-- end
-- calcPlugin["frexp"] = frexp

-- 返回 x*2^y
local function ldexp(x, y)
  return math.ldexp(x, y)
end
calcPlugin["ldexp"] = ldexp

-- 返回 e^x
local function exp(x)
  return math.exp(x)
end
calcPlugin["exp"] = exp

-- 返回x的平方根 e.g. sqrt(x) = x^0.5
local function sqrt(x)
  return math.sqrt(x)
end
calcPlugin["sqrt"] = sqrt

-- x为底的对数, log(10, 100) = log(100) / log(10) = 2
local function log(x, y)
  -- 不能为负数或0
  if x <= 0 or y <= 0 then return nil end

  return math.log(y) / math.log(x)
end
calcPlugin["log"] = log

-- 自然数e为底的对数
local function loge(x)
  -- 不能为负数或0
  if x <= 0 then return nil end

  return math.log(x)
end
calcPlugin["loge"] = loge

-- 10为底的对数
local function log10(x)
  -- 不能为负数或0
  if x <= 0 then return nil end

  return math.log10(x)
end
calcPlugin["log10"] = log10

-- 平均值
local function avg(...)
  local data = {...}
  local n = select("#", ...)
  -- 样本数量不能为0
  if n == 0 then return nil end

  -- 计算总和
  local sum = 0
  for _, value in ipairs(data) do
    sum = sum + value
  end

  return sum / n
end
calcPlugin["avg"] = avg

-- 方差
local function variance(...)
  local data = {...}
  local n = select("#", ...)
  -- 样本数量不能为0
  if n == 0 then return nil end

  -- 计算均值
  local sum = 0
  for _, value in ipairs(data) do
    sum = sum + value
  end
  local mean = sum / n

  -- 计算方差
  local sum_squared_diff = 0
  for _, value in ipairs(data) do
    sum_squared_diff = sum_squared_diff + (value - mean)^2
  end

  return sum_squared_diff / n
end
calcPlugin["var"] = variance

-- 阶乘
local function factorial(x)
  -- 不能为负数
  if x < 0 then return nil end
  if x == 0 or x == 1 then return 1 end

  local result = 1
  for i = 1, x do
    result = result * i
  end

  return result
end
calcPlugin["fact"] = factorial

-- 实现阶乘计算(!)
local function replaceToFactorial(str)
  -- 替换[0-9]!字符为fact([0-9])以实现阶乘
  return str:gsub("([0-9]+)!", "fact(%1)")
end

-- 简单计算器
function M.func(input, seg, env)
  local prefix = nil
  for _, candidatePrefix in ipairs(M.prefixes) do
    if startsWith(input, candidatePrefix) then
      prefix = candidatePrefix
      break
    end
  end
  if not prefix then return end
  -- 提取算式
  local express = truncateFromStart(input, prefix)
  -- 算式长度 < 2 直接终止(没有计算意义)
  if (string.len(express) < 2) then return end
  -- moqi 补丁：算式必须含数字——纯字母的 v 开头普通输入直接返回，
  -- 否则 load() 求值为 nil，Candidate(text=nil) 每次报 Lua 错误。
  if not express:match("%d") then return end
  local code = replaceToFactorial(express)

  local success, result = pcall(load("return " .. code, "calculate", "t", calcPlugin))
  -- moqi 补丁：result 为 nil 时走失败分支，避免 Candidate 收到 nil。
  if success and result ~= nil then
    yield(Candidate(input, seg.start, seg._end, result, ""))
    yield(Candidate(input, seg.start, seg._end, express .. "=" .. result, ""))
  else
    yield(Candidate(input, seg.start, seg._end, express, "解析失败"))
    yield(Candidate(input, seg.start, seg._end, code, "入参"))
  end
end

return M
