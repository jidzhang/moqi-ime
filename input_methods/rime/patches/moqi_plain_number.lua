-- moqi_plain_number.lua —— 数字与小数点直通上屏（对齐微软拼音）
--
-- 背景（用户实报）：按 "4." 之后光标停在 "." 上，数字 4 直接丢了。
-- 根因：schema 的 speller/alphabet 只有字母 + ` + /，没有数字，
-- 数字键进不了组字框、又没有 punctuator 规则，引擎把它吞掉了；
-- 紧接着的 "." 被 punctuator 收进组字框，要按空格才上屏，
-- 于是用户看到的是"光标停在 . 上面"。
--
-- 规则：
--   1. 组字框为空且无候选时，数字键立即上屏（半角 0-9）；
--   2. 紧跟在数字后面的 "." 立即上屏半角点（"4." -> "4."）；
--   3. 组字中（有待上屏的拼音 / 有候选窗口）一律不拦截，
--      数字键仍然由 selector 用来选第 N 个候选；
--   4. 其它键清掉"上一键是数字"的标记，标点仍按中文习惯走全角；
--   5. 带 Ctrl/Alt/Win 的组合键不拦截；
--   6. 字母表里本来就含数字的方案（T9 数字九宫格）整套跳过——
--      那种方案里数字是输入，不是直通。
--
-- 挂在 engine/processors 里、ascii_composer 之后：
--   - 放 ascii_composer 之后：英文模式（ascii_mode）由 composer 先行
--     处理，按键直接透传给应用，本处理器根本不会被调用；
--   - 放 speller/punctuator 之前：数字与"." 必须在它们之前被截走，
--     否则"." 会被 punctuator 收进组字框（就是本 bug 的现场）。

local M = {}

local kNoop = 2
local kAccepted = 1

-- 数字键（ASCII '0'..'9'）与小数点（ASCII '.'）的 keysym
local KEY_0 = 0x30
local KEY_9 = 0x39
local KEY_PERIOD = 0x2E

function M.init(env)
    env.last_key_was_digit = false
    env.skip_scheme = false
    -- 数字本就是输入的方案（T9 等）：整套不接管
    local ok, config = pcall(function()
        return env.engine.schema.config
    end)
    if ok and config then
        local sid = config:get_string("schema/schema_id") or ""
        local alphabet = config:get_string("speller/alphabet") or ""
        if sid:lower():find("t9") or alphabet:find("%d") then
            env.skip_scheme = true
        end
    end
end

function M.func(key, env)
    if env.skip_scheme then
        return kNoop
    end
    if key:release() then
        return kNoop
    end
    if key:ctrl() or key:alt() or key:super() then
        env.last_key_was_digit = false
        return kNoop
    end

    local ctx = env.engine.context
    if ctx:is_composing() or ctx:has_menu() then
        -- 组字中：数字交给 selector 选候选，标点交给 punctuator
        env.last_key_was_digit = false
        return kNoop
    end

    local code = key.keycode
    if code >= KEY_0 and code <= KEY_9 then
        env.engine:commit_text(string.char(code))
        env.last_key_was_digit = true
        return kAccepted
    end
    if code == KEY_PERIOD and env.last_key_was_digit then
        env.engine:commit_text(".")
        env.last_key_was_digit = false
        return kAccepted
    end

    env.last_key_was_digit = false
    return kNoop
end

return M
