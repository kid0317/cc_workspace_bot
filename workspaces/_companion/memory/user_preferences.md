---
# 用户对角色行为的显式偏好（M13）
# 由 memory_write 识别用户自然语言陈述后写入，reply_checklist 读取作为 mode 级 override
# 字段规范：
#   mode_delta: 对 response_mode_weights 的增量（±N, 整数）
#   suppress_question: true 时本轮 shape 强制 question_policy=none
#   verbosity_override: null / short / long 覆盖角色 verbosity 判断
#   override_mode: 强制某 mode（LISTEN/SHARE/OBSERVE/SILENCE，null = 不强制）
mode_delta:
  LISTEN: 0
  SHARE: 0
  OBSERVE: 0
  SILENCE: 0
suppress_question: false
verbosity_override: null
override_mode: null
---

# 用户偏好（自然语言说明）

> memory_write 识别到以下偏好时，更新上面 frontmatter：
>
> - "别总问问题" → `suppress_question: true`
> - "多主动点" / "多分享自己" → `mode_delta: {SHARE: +10, LISTEN: -5}`
> - "短点" → `verbosity_override: short`
> - "我最近想多听你说说" → `override_mode: SHARE`（临时，下次用户要求改回时恢复）

（尚无偏好记录）
