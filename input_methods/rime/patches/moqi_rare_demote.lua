-- moqi-prebuild: 生僻字降位（2026-10-22）
-- 背景：上游默认挂载 41448/GB18030-2022 字表，fe/tin/bun/won/den 等输入
-- 被精确音节匹配的生僻字（𫸴/𬽞/𡎛/𥥈/㩐…）霸占首名，且这些字走系统
-- 后备字体渲染、字号巨大（用户实报，bug-list #7/#9）。
-- 规则：候选文本含 Ext-A(U+3400–4DBF) 或 Ext-B 及以上(U+20000–3FFFF) 字元
-- 即沉到候选流末尾（保持相对顺序）——首页干净，翻页仍可达（用户拍板：
-- 不能完全关，椑/𫸴 这类字要能打出来）。
-- 性能：懒迭代逐候选判断，常见候选直接 yield 不缓存；翻到末页时生僻字
-- 才被拉出。无字表嵌入，无额外内存。
local function is_rare(text)
    for _, code in utf8.codes(text) do
        if (code >= 0x3400 and code <= 0x4DBF) or (code >= 0x20000 and code <= 0x3FFFF) then
            return true
        end
    end
    return false
end

local function filter(input, env)
    local rare = {}
    for cand in input:iter() do
        if is_rare(cand.text) then
            rare[#rare + 1] = cand
        else
            yield(cand)
        end
    end
    for i = 1, #rare do
        yield(rare[i])
    end
end

return filter
