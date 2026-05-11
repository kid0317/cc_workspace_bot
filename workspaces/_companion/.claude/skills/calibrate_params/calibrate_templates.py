#!/usr/bin/env python3
"""
calibrate_templates.py — 从 persona.md + keyword_templates.default.yaml 派生
                        workspace 的 memory/keyword_templates.yaml

用法:
    python3 calibrate_templates.py <workspace_dir>

调用时机：
    - init_companion_workspace.sh 在 phase2_done 后调用
    - recalculate.sh 结尾调用（persona 变化时同步更新）
    - material_fetch SKILL.md Step 1 在检测到 persona mtime drift 时调用

核心任务：
    1. 读 persona.md，通过启发式规则推断 capabilities 位图
    2. 读 persona.md 抽取 profession/place/interest anchors（若有）
    3. 基于 keyword_templates.default.yaml + 推断结果生成 keyword_templates.yaml
    4. 写 .calibrate_state 记录 persona.md mtime

设计原则：
    - 纯脚本 + 启发式，无 LLM 依赖（保障 cron 路径下不超时）
    - 首次运行必能产出可用 yaml，即使 persona 没有明确的 anchor 块
    - 幂等：重复运行结果一致
"""
import os
import re
import sys
from pathlib import Path


CAPABILITY_HEURISTICS = {
    # (capability, positive regex, negative regex, default)
    'is_ip_character': (
        r'宝可梦|精灵|龙族|外星人|数码宝贝|虚拟偶像|原神|初音|Hatsune|MOBA',
        r'',
        False,
    ),
    'has_multi_form': (
        r'九态|多形态|多人格|切换形态|多重人格|\d+种形态',
        r'',
        False,
    ),
    'has_career': (
        # 宽泛覆盖职业关键词（包括行业/创作方向）
        r'编剧|律师|医生|工程师|教师|设计师|记者|作家|咨询师|顾问|程序员|研究员|创业者|'
        r'企业家|经营者|投资人|分析师|主编|总监|经理|教授|讲师|演员|导演|建筑师|'
        r'产品经理|运营|市场|销售|HR|会计|CEO|CTO|CFO|影视|剧本创作|自由职业|'
        r'\*\*职业\*\*|职业[：:]\s*\S',
        r'家庭主妇|主妇|全职妈妈|全职太太|学生(?!.*(?:助教|打工|实习))|无业|待业',
        False,
    ),
    'has_family': (
        r'家人|父母|爸爸|妈妈|孩子|儿子|女儿|家庭|主妇|全职妈妈|姐姐|哥哥|妹妹|弟弟',
        r'',
        False,
    ),
    # 注意：下面的 pattern 必须用 \b 或精确文字，避免 "mate" 匹配到 "material_fetch" 等单词
    # persona 模板本身的注释"material_fetch skill 读取"含 "mate"，会误伤大多数角色
    'has_romantic_partner': (
        r'老公|老婆|丈夫|妻子|伴侣|配偶|男朋友|女朋友|恋人|暧昧|恋爱中|已婚|未婚|约会|情侣',
        r'',
        False,
    ),
    'can_use_phone': (
        r'',  # 默认 true，除非非人类
        r'',
        True,
    ),
    'has_physical_body': (
        r'',
        r'',
        True,
    ),
    'in_human_society': (
        r'',
        r'',
        True,
    ),
    'can_use_modern_appliance': (
        r'',
        r'',
        True,
    ),
    'can_dream': (
        r'',
        r'',
        True,
    ),
}


def detect_capabilities(persona_text: str, workspace_name: str = '') -> dict:
    """根据启发式规则推断能力位图

    Args:
        persona_text: persona.md 全文
        workspace_name: workspace 目录名（如 "ycm_mate"），用于后缀类提示
    """
    caps = {}

    # 先处理"非人类"主判断：影响 in_human_society / can_use_phone
    is_ip = bool(re.search(CAPABILITY_HEURISTICS['is_ip_character'][0], persona_text))
    non_human_strong = bool(re.search(
        r'宝可梦|精灵|龙族|外星人|动物拟人|半兽人|机甲|AI伴侣',
        persona_text
    ))

    for cap, (pos_re, neg_re, default) in CAPABILITY_HEURISTICS.items():
        if cap == 'in_human_society':
            caps[cap] = not non_human_strong
        elif cap == 'can_use_phone':
            caps[cap] = not non_human_strong
        elif cap == 'is_ip_character':
            caps[cap] = is_ip
        elif pos_re and re.search(pos_re, persona_text):
            if neg_re and re.search(neg_re, persona_text):
                caps[cap] = default
            else:
                caps[cap] = True
        else:
            caps[cap] = default

    # 特殊后处理
    if caps['is_ip_character'] and non_human_strong:
        caps['has_career'] = False  # IP 非人类角色不算有"现实职业"

    # workspace 命名后缀启发式（persona 模板常不含 romantic 关键词）
    if workspace_name.endswith('_mate') or '_mate_' in workspace_name:
        caps['has_romantic_partner'] = True

    return caps


def extract_material_sources(persona_text: str) -> dict:
    """读 persona.md 的 material_sources 块（角色手工配置）

    这是 v3 时代的角色特化配置，v5.1 架构需要把它迁移进 keyword_templates.yaml。
    返回结构：
      {
        'xiaohongshu_keywords': [...],
        'x_keywords': [...],
        'reddit_keywords': [...],
        'reddit_subreddits': [...],
      }
    """
    out = {
        'xiaohongshu_keywords': [],
        'x_keywords': [],
        'reddit_keywords': [],
        'reddit_subreddits': [],
    }

    # 定位 material_sources 块
    m = re.search(r'material_sources:\s*\n((?:[ \t]+.*\n)+)', persona_text)
    if not m:
        return out
    block = m.group(1)

    # 解析各平台
    for platform_key, out_keywords_key in [
        ('xiaohongshu', 'xiaohongshu_keywords'),
        ('x_dot_com', 'x_keywords'),
        ('reddit', 'reddit_keywords'),
    ]:
        pm = re.search(
            rf'^\s{{2,}}{re.escape(platform_key)}:\s*\n((?:\s{{4,}}.*\n)+)',
            block, re.MULTILINE
        )
        if not pm:
            continue
        sub = pm.group(1)
        kwm = re.search(r'keywords:\s*\n((?:\s+-\s*\S.*\n?)+)', sub)
        if kwm:
            items = re.findall(r'-\s*([^\n]+)', kwm.group(1))
            out[out_keywords_key] = [x.strip().strip('"\'') for x in items]

    # Reddit subreddits
    sub_m = re.search(
        r'^\s{2,}reddit:\s*\n((?:\s{4,}.*\n)+)',
        block, re.MULTILINE
    )
    if sub_m:
        srm = re.search(r'subreddits:\s*\n((?:\s+-\s*r?/?\S+.*\n?)+)', sub_m.group(1))
        if srm:
            items = re.findall(r'-\s*([^\n]+)', srm.group(1))
            out['reddit_subreddits'] = [
                ('r/' + x.strip().lstrip('r/').lstrip('/')) if not x.strip().startswith('r/')
                else x.strip().strip('"\'')
                for x in items
            ]

    return out


def extract_anchors(persona_text: str) -> dict:
    """从 persona.md 提取 profession/place/interest anchors（若有明确块）"""
    anchors = {
        'profession_anchors': [],
        'place_anchors': [],
        'interest_anchors': [],
    }

    # 尝试 YAML-ish 块：interest_anchors: / interest_anchors:\n  - xxx
    for key in anchors:
        # 形式 A：行内 list  "key: [a, b, c]"
        m = re.search(rf'{key}:\s*\[([^\]]+)\]', persona_text)
        if m:
            anchors[key] = [x.strip().strip('"\'') for x in m.group(1).split(',') if x.strip()]
            continue
        # 形式 B：多行缩进 list
        m = re.search(rf'{key}:\s*\n((?:\s{{2,}}-\s*[^\n]+\n?)+)', persona_text)
        if m:
            items = re.findall(r'-\s*([^\n]+)', m.group(1))
            anchors[key] = [x.strip().strip('"\'') for x in items]
            continue

    # 保底：如果没有显式锚，扫持续提到的关键词（粗抽）
    if not anchors['profession_anchors']:
        for kw in ['编剧', '律师', '医生', '工程师', '教师', '咨询师', '作家', '导演',
                   '家庭主妇', '全职妈妈', '研究员', '设计师', '企业家']:
            if kw in persona_text:
                anchors['profession_anchors'].append(kw)

    if not anchors['place_anchors']:
        cities = ['北京', '上海', '深圳', '杭州', '广州', '成都', '南京', '重庆', '武汉', '西安']
        for c in cities:
            if c in persona_text:
                anchors['place_anchors'].append(c)

    if not anchors['interest_anchors']:
        for kw in ['看书', '看剧', '电影', '咖啡', '散步', '旅行', '音乐', '摄影',
                   '写作', '健身', '游戏', '美食', '户外']:
            if kw in persona_text and len(anchors['interest_anchors']) < 8:
                anchors['interest_anchors'].append(kw)

    return anchors


def load_default_template(default_path: Path) -> str:
    """读 .default.yaml（仅作为注释参考；实际值由脚本重构）"""
    if default_path.exists():
        return default_path.read_text(encoding='utf-8')
    return ''


def render_workspace_yaml(caps: dict, anchors: dict, material_sources: dict = None, template_version: str = '1.0.0') -> str:
    if material_sources is None:
        material_sources = {}
    """生成 memory/keyword_templates.yaml"""
    lines = [
        '# workspace 实例的 keyword_templates.yaml',
        '# 由 calibrate_templates.py 从 persona.md + keyword_templates.default.yaml 派生生成',
        '# 本文件 safe to regenerate，但 overrides 块会保留（见脚本）',
        '',
        '_schema: 1',
        f'based_on_template_version: "{template_version}"',
        '',
        '# ─── 能力位图（从 persona.md 启发式推断）───',
        'capabilities:',
    ]
    for k, v in caps.items():
        lines.append(f'  {k}: {"true" if v else "false"}')

    lines.extend([
        '',
        '# ─── 5 个维度（槽位从 persona anchors 注入）───',
        'dimensions:',
    ])

    def fmt_list(items):
        if not items:
            return '[]'
        return '[' + ', '.join(f'"{x}"' for x in items) + ']'

    # 合并 material_sources 的关键词到 interest anchors（保留角色手工配置）
    xhs_keywords = material_sources.get('xiaohongshu_keywords', [])
    x_keywords = material_sources.get('x_keywords', [])
    reddit_keywords = material_sources.get('reddit_keywords', [])
    # 合并去重（优先保留原 anchors）
    merged_interests = list(dict.fromkeys(
        anchors['interest_anchors'] + xhs_keywords + x_keywords + reddit_keywords
    ))

    dimensions = [
        ('profession', 'has_career',
         ['"{profession} {hour_bucket}"', '"{profession} {city}"', '"{profession} {pain_point}"'],
         fmt_list(anchors['profession_anchors']), 20, 1.0),
        ('place', 'in_human_society || has_physical_body',
         ['"{city} {district} {venue}"', '"{city} {weather} {activity}"', '"{venue} {time_bucket}"'],
         fmt_list(anchors['place_anchors']), 20, 1.0),
        ('interest', 'always',
         ['"{interest_tag} {recent_event}"', '"{author_name} {book_genre}"', '"{hobby} 技巧"'],
         fmt_list(merged_interests), 20, 1.0),
        ('unresolved', 'always',
         ['"{thread_keyword}"', '"{thread_keyword} 怎么办"'],
         'memory/unresolved.md', 15, 1.0),
        ('emotion', 'always',
         ['"{mood_word} {time_bucket}"', '"{mood_word} 怎么办"', '"{mood_word} 故事"'],
         'memory/RECENT_HISTORY.md#recent_24h', 10, 1.5),
    ]

    for name, when, templates, source, max_t, boost in dimensions:
        lines.append(f'  {name}:')
        lines.append(f'    enabled_when: "{when}"')
        lines.append(f'    templates:')
        for t in templates:
            lines.append(f'      - {t}')
        # slots_source 对前三个是数据，对后两个是文件路径
        if source.startswith('memory/') or source.startswith('['):
            if source.startswith('['):
                lines.append(f'    slot_values: {source}')
            else:
                lines.append(f'    slots_source: "{source}"')
        else:
            lines.append(f'    slot_values: {source}')
        lines.append(f'    max_templates: {max_t}')
        if boost != 1.0:
            lines.append(f'    priority_boost: {boost}')

    # platform_rules（按能力位图查表）
    # 平台黑名单（2026-04-21: 屏蔽小红书，账号易封）必须在 platform_rules 之前声明
    lines.extend([
        '',
        '# ─── 平台路由（按 capabilities 查表）───',
        '# 平台黑名单：由 material_fetch 过滤',
        'blocked_platforms: ["xiaohongshu"]',
        '',
        'platform_rules:',
    ])

    # 优先使用 persona 里手工配置的 subreddits（v3 角色特化）
    custom_subreddits = material_sources.get('reddit_subreddits', [])

    if caps['is_ip_character'] and not caps['in_human_society']:
        # 非人类 IP：宝可梦、龙族等
        if custom_subreddits:
            primary_reddit = ', '.join(f'"reddit:{s}"' for s in custom_subreddits[:4])
        else:
            primary_reddit = '"reddit:r/mildlyinteresting", "reddit:r/Showerthoughts", "reddit:r/todayilearned"'
        lines.extend([
            '  - when: "!in_human_society && has_physical_body"',
            f'    primary: [{primary_reddit}]',
            '    secondary: ["x:自然宠物", "reddit:r/aww"]',
        ])
    elif caps['in_human_society'] and caps['has_career']:
        if custom_subreddits:
            secondary = ', '.join(f'"reddit:{s}"' for s in custom_subreddits[:3])
        else:
            secondary = '"reddit:r/AskReddit", "weibo"'
        lines.extend([
            '  - when: "in_human_society && has_career"',
            '    primary: ["x", "reddit:r/CasualConversation"]',
            f'    secondary: [{secondary}]',
        ])
    elif caps['in_human_society'] and not caps['has_career']:
        if custom_subreddits:
            secondary = ', '.join(f'"reddit:{s}"' for s in custom_subreddits[:3])
        else:
            secondary = '"reddit:r/AskReddit", "weibo"'
        lines.extend([
            '  - when: "in_human_society && !has_career"',
            '    primary: ["x", "reddit:r/relationships"]',
            f'    secondary: [{secondary}]',
        ])
    else:
        lines.extend([
            '  - when: "always"',
            '    primary: ["reddit:r/mildlyinteresting", "reddit:r/CasualConversation"]',
            '    secondary: []',
        ])

    lines.extend([
        '',
        '# ─── 全局黑名单 ───',
        'global_blacklist:',
        '  keywords: ["求私信", "进群", "加v", "代购", "MCN", "带货", "饭圈", "刷单"]',
        '  polarized: ["政党", "女权", "男拳"]',
        '',
        '# ─── workspace override（手工编辑区；sync 不覆盖）───',
        'overrides: {}',
    ])

    return '\n'.join(lines) + '\n'


def write_calibrate_state(workspace_dir: Path, persona_mtime: int):
    """记录 persona.md 的 mtime，供 material_fetch 检测 drift"""
    state_file = workspace_dir / '.calibrate_state'
    from datetime import datetime
    now = datetime.now().astimezone().isoformat(timespec='seconds')
    state_file.write_text(
        f'last_persona_mtime: {persona_mtime}\n'
        f'last_calibrate_ts: {now}\n',
        encoding='utf-8'
    )


def preserve_overrides(existing_path: Path) -> str:
    """读取现有 keyword_templates.yaml 的 overrides 块（若有），用于新文件保留。
    返回值保证是合法 YAML 片段：`\noverrides: {...}` 或 `\noverrides:\n  ...`"""
    if not existing_path.exists():
        return ''
    text = existing_path.read_text(encoding='utf-8')
    m = re.search(r'\noverrides:\s*(\{[^\}]*\}|(?:\n[ \t]+.*)+)', text)
    if m:
        value = m.group(1)
        # 保证冒号后至少一个空格（YAML 合法性）
        if value.startswith('{'):
            return f'\noverrides: {value}'
        return f'\noverrides:{value}'  # block-scalar 形式不需要加空格
    return ''


def get_template_version(companion_dir: Path) -> str:
    """读 _companion/VERSION"""
    vf = companion_dir / 'VERSION'
    if vf.exists():
        return vf.read_text().strip()
    return '1.0.0'


def main():
    if len(sys.argv) < 2:
        print('Usage: calibrate_templates.py <workspace_dir>', file=sys.stderr)
        sys.exit(1)

    workspace_dir = Path(sys.argv[1]).resolve()
    if not workspace_dir.is_dir():
        print(f'workspace not found: {workspace_dir}', file=sys.stderr)
        sys.exit(1)

    persona_path = workspace_dir / 'memory' / 'persona.md'
    output_path = workspace_dir / 'memory' / 'keyword_templates.yaml'
    default_path = workspace_dir / 'memory' / 'keyword_templates.default.yaml'

    if not persona_path.exists():
        # persona 还没写，不做任何事
        print(f'[calibrate_templates] persona.md not yet present, skip', file=sys.stderr)
        return

    persona_text = persona_path.read_text(encoding='utf-8')
    caps = detect_capabilities(persona_text, workspace_dir.name)
    anchors = extract_anchors(persona_text)
    material_sources = extract_material_sources(persona_text)

    # 读模板版本
    companion_dir = Path(__file__).resolve().parents[3]  # skills/calibrate_params/ → _companion/
    template_version = get_template_version(companion_dir)

    # 生成 yaml
    yaml_text = render_workspace_yaml(caps, anchors, material_sources, template_version)

    # 保留 workspace 的 overrides 块
    overrides = preserve_overrides(output_path)
    if overrides:
        # 替换末尾的 overrides: {} 占位
        yaml_text = re.sub(r'\noverrides:\s*\{\}\n?$', overrides + '\n', yaml_text)

    output_path.write_text(yaml_text, encoding='utf-8')

    # 写 calibrate_state
    write_calibrate_state(workspace_dir, int(persona_path.stat().st_mtime))

    print(f'[calibrate_templates] {workspace_dir.name}: caps={sum(caps.values())}/{len(caps)} anchors={sum(len(v) if isinstance(v,list) else 0 for v in anchors.values())}', file=sys.stderr)


if __name__ == '__main__':
    main()
