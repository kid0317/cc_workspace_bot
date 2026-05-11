# 未完挂念

> life_sim 生成前读取，决定是否推进某条挂念。
> memory_distill 从对话与 life_log 中抽取新条目；life_sim 写 last_touched。
>
> 字段：
>   id         [U001] 用户倾诉类 / [M001] 素材派生类
>   src        user_told | material_derived
>   kind       user_told | work_line | relational | chore
>   decay_d    自 last_touched 后多少天无触及则标"已淡忘"（kind 默认：user_told=never / work_line=90 / relational=60 / chore=30）
>   last_touched  life_sim 唯一写入；首次添加写 (never)；被 life_log 引用时更新为该条目 id（如 L013）
>
> 规则：
>   - user_told 的条目**永不淡忘**，只能通过用户显式告知归档（由 memory_distill 语义判断）
>   - material_derived 的条目按 decay_d 淡忘，淡忘后移到文件末尾的"已淡忘"块（保留作为历史）
>   - 同一 user_told 条目被强制呼应后 72h 内不再强制（避免"妈妈生病"每 4h 被提一次）

---

<!-- 活跃挂念（life_sim 读取此块） -->
<!-- 格式参考（首次初始化时整节为空；memory_distill 和手工 bootstrap 会追加条目）：
     - [U001] src=user_told kind=user_told decay_d=never last_touched=(never) forced_echo_last=(never)  妈妈上周说血压又高了
     - [M001] src=material_derived kind=work_line decay_d=90 last_touched=L010 剧本里那句别扭的对白
-->


<!-- ────────────────────────────────────────── -->

## 已淡忘

> material_derived 超过 decay_d 未触及后移到这里。保留历史，不再作为 seed。

