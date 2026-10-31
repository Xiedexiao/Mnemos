# AI 本体与知识图谱方向优秀开源项目调研报告

> 服务对象：Mnemos（WeKnora fork，Go + Vue + Neo4j + ES）B 期"AI 本体与知识图谱"落地
> 对齐设计稿：KnowAxis V3.0 §8-9（AI 本体与知识图谱一体化：SchemaProposal / 置信度三级 / GraphCommit / 图应用）
> 调研日期：2026-10-31；star 数与最近提交均取自 GitHub API 当日数据
> 说明：本报告只做调研与方案建议，不改动任何代码

---

## 0. 调研背景与目标

Mnemos B 期要补齐的能力（来自 KnowAxis V3.0 §8-9）：

1. **本体（Schema）**：类型 / 属性 / 关系 / 约束 / 别名；AI 文档驱动自动建模 → SchemaProposal → 兼容性检查 → 发布 + 回归。
2. **图谱构建**：实体归一、关系抽取（每条边带置信度 + 原文证据绑定），置信度三级 EXTRACTED / INFERRED / AMBIGUOUS。
3. **GraphCommit**：图谱变更集版本化、可回放、可回滚、灰度。
4. **图应用**：按实体探索、路径查询、影响分析、专家发现、社区检测、中心度。
5. **增量抽取缓存**：内容 hash 未变则跳过。

当前基线（代码事实，后文建议均以此为出发点）：

- 抽取产物结构 `internal/types/extract_graph.go`：`GraphNode{Name, Chunks[], Attributes[]}`、`GraphRelation{Node1, Node2, Type}`、`GraphData{Text, Node[], Relation[]}`——**只有名字级粗抽，无类型约束、无置信度、无证据 locator**。
- Neo4j 写入 `internal/application/repository/retriever/neo4j/repository.go`：用 APOC `apoc.merge.node(row.labels, {name, kg})` / `apoc.merge.relationship(...)` 合并，labels 来自 `NameSpace{KnowledgeBase, Knowledge}`；删除按 `knowledge_id` **整库清空重建**，无增量、无变更集。
- 已有可复用资产：`internal/types/source_locator.go` 的 `SourceLocator{Type(pdf/docx/slide/sheet/text/time/section), SourceID, SourceHash, Page, BBox, Block, Slide, Sheet, RowStart/RowEnd, Start/End, Quote}` 与 `internal/sourceloc/`（align / assign / remap）的对齐机制——**证据绑定可以直接复用它，不需要发明新机制**。

---

## 1. 调研项目分述

### 1.1 OpenSPG / KAG（蚂蚁集团 + OpenKG）

- **定位一句话**：业界少有的"强 Schema 约束下用 LLM 建图谱"的完整开源引擎，KAG 是其上的逻辑形式引导推理问答框架。
- **许可证**：Apache-2.0。
- **活跃度**：KAG 9,140 stars（最近提交 2026-01-28）；OpenSPG 引擎 2,285 stars（最近提交 2025-07-05）。⚠️ 引擎仓库半年以上无提交，活跃度在放缓，但设计文档与代码仍是 schema 建模方向最完整的参考。

**核心机制**

| 维度 | 做法 |
|---|---|
| Schema 建模 | SPG 框架在属性图（LPG）之上做语义增强：主体模型（standardNode）、演化模型、谓词模型三件套约束知识结构；schema 兼容"强 Schema 专业知识"与"弱 Schema 场景"（LLMFriSPG），实体与事件类型可共用知识类型定义（来源：[OpenSPG README](https://github.com/OpenSPG/openspg)、[KAG README](https://github.com/OpenSPG/KAG)、[KAG 论文 arXiv:2409.13731](https://arxiv.org/abs/2409.13731)） |
| 抽取管线 | SPG-Builder 知识构建算子框架：布局分析 → 知识抽取 → 属性归一化 → 语义对齐（实体链接、概念标准化、实体归一算子），保证实例唯一性；KNext 提供 schema 约束下的 LLM 增强抽取；v0.7 提供 Lightweight Build（构建 token 成本降 89%） |
| 逻辑规则 | SPG-Reasoner 抽象出 KGDSL（Knowledge Graph DSL），用接近自然语言的声明式语法定义 logicalRule，做规则推理与符号-神经融合推理（来源：[OpenSPG README](https://github.com/OpenSPG/openspg)） |
| 置信度/证据 | 无显式三级置信度体系；靠"概念语义对齐 + schema 约束"降低 OpenIE 噪声（KAG 的出发点即克服向量相似度歧义与 OpenIE 噪声） |
| 版本化/增量 | 社区解析文章指出其构建任务在属性粒度上采用 append（追加）而非 overwrite（覆盖）（来源：GitCode 社区文章《OpenSPG 知识图谱增量更新机制解析》，可信度中等）；知识-文本互索引（chunk 与图谱结构互查） |

**对 Mnemos 可借鉴**

1. **Schema 三要素分层建模**：主体模型（类型及其 standardNode 结构）/ 谓词模型（关系及其两端约束）/ 属性约束，与 KnowAxis 的 ontology_types / ontology_relations / ontology_properties 三表设计一一对应，验证了表结构拆法的合理性。
2. **schema 约束抽取**：把已发布 schema 注入抽取 prompt、约束 LLM 输出必须在类型/关系枚举内——这是 KAG 抑制噪声的核心手段，B 期应照此改造现有抽取 prompt。
3. **实体归一算子化**：实体链接、概念标准化、归一作为独立算子串进管线，而非一次性 LLM 调用。
4. **逻辑规则 DSL 思想**（KGDSL）：B 期可用受限版实现（如"推导边标记为 INFERRED + 规则 id"），不必上完整 DSL。

来源：[OpenSPG/openspg](https://github.com/OpenSPG/openspg) | [OpenSPG/KAG](https://github.com/OpenSPG/KAG) | [KAG 论文](https://arxiv.org/abs/2409.13731) | [项目主页 spg.openkg.cn](https://spg.openkg.cn/en-US) | [中文文档（语雀）](https://openspg.yuque.com/ndx6g9/docs_en)

---

### 1.2 Microsoft GraphRAG

- **定位一句话**：结构化、层次化 RAG 的工业参考实现——把非结构化文本建成"实体/关系带描述与权重、可做社区摘要"的知识图谱。
- **许可证**：MIT。
- **活跃度**：36,504 stars，最近提交 2026-10-29，非常活跃（模块化重构后的 v2 管线持续迭代）。

**核心机制**（来源：[官方 Methods 文档](https://microsoft.github.io/graphrag/index/methods/)、[Default Dataflow 文档](https://microsoft.github.io/graphrag/index/default_dataflow/)）

| 维度 | 做法 |
|---|---|
| 管线阶段 | Documents → **TextUnits**（默认 1200 token，抽取粒度与溯源单位）→ 实体抽取（每个 TextUnit 提示 LLM 抽"命名实体 + 描述"，受 `entity_types` 配置约束）→ 关系抽取（实体对 + 关系描述）→ 描述合并（同一实体/关系跨 TextUnit 的描述数组由 LLM 汇总为一条）→（可选）Claim/Covariate 抽取（带状态评估与时间边界的事实断言，链接回 TextUnit）→ Leiden 层次化社区检测（递归到社区规模阈值）→ 社区报告（执行摘要 + 关键实体/关系/引用）→ 嵌入 |
| 证据（provenance） | 实体表带 `text_unit_ids` 列表（该实体出现在哪些 TextUnit）；document↔textunit 映射保留用于下游引用；关系带 `weight`、`combined_degree`、`degree` 统计字段 |
| 置信度 | 无三级置信度；用 weight / degree 等"强度"字段近似 |
| 版本化/增量 | 官方索引管线为批式重建，无 commit/回滚概念；增量靠重新跑 pipeline |
| 两种模式 | Standard（全 LLM）与 Fast（NLP 名词短语 + 共现边，50–100 token 细 chunk，跳过摘要与 claim，便宜一个量级）；图抽取约占索引成本 75% |

**对 Mnemos 可借鉴**

1. **TextUnit = 抽取与溯源的统一粒度**：实体和关系都挂 text_unit_ids，B 期的 evidence 绑定直接映射到 Mnemos 的 chunk_id + SourceLocators（Mnemos 的 chunk 已经带 SourceLocators，比 GraphRAG 更强，能定位回原始文件页/区域）。
2. **`entity_types` 配置约束抽取**：GraphRAG 用一个实体类型枚举列表约束抽取范围；B 期把它升级为"已发布 schema 的类型 + 关系枚举"即可。
3. **描述合并（summarization on merge）**：同一实体多来源描述先攒数组、再 LLM 汇总成单条——对应 Mnemos 实体归一后 description 的合并策略。
4. **Claim/Covariate 独立抽取**：把"事实断言"作为独立对象（带状态与时间边界）而不是藏在关系描述里，与 KnowAxis 的 graph_facts 定位一致。
5. **成本意识**：图抽取占 75% 成本 → 必须做增量缓存（hash 未变跳过），Fast 模式证明"便宜路径"对全局性问答也够用。

来源：[microsoft/graphrag](https://github.com/microsoft/graphrag) | [Methods](https://microsoft.github.io/graphrag/index/methods/) | [Default Dataflow](https://microsoft.github.io/graphrag/index/default_dataflow/) | [Microsoft Research 项目页](https://www.microsoft.com/en-us/research/project/graphrag)

---

### 1.3 LightRAG（HKUDS）

- **定位一句话**：双层（实体层/关系层）抽取与检索的轻量 GraphRAG，增量插入、删除与缓存机制是同类项目里最完整的。
- **许可证**：MIT。
- **活跃度**：40,446 stars，EMNLP 2025，最近提交 2026-10-29，非常活跃。

**核心机制**（来源：[HKUDS/LightRAG README](https://github.com/HKUDS/LightRAG) 及仓库 issues）

| 维度 | 做法 |
|---|---|
| 抽取管线 | 4 种 chunk 策略（固定长度/递归/向量/段落语义）→ 每 chunk LLM 抽实体+关系；抽取/查询/关键词/VLM 可配独立 LLM；支持 JSON mode 稳定输出 |
| 实体归一 | entity-merge / relation-merge 两阶段：按精确 key 匹配去重合并（社区 issue 明确当前是 exact key matching，[Issue #1631](https://github.com/HKUDS/LightRAG/issues/1631)），合并时描述可触发 LLM 重汇总（`FORCE_LLM_SUMMARY_ON_MERGE`） |
| 证据（provenance） | 实体/关系带 `source_id`（chunk id 列表）；`MAX_SOURCE_IDS_PER_RELATION`、`SOURCE_IDS_LIMIT_METHOD`、`MAX_FILE_PATHS` 限制溯源列表膨胀 |
| 增量/缓存 | 抽取 LLM 结果进 KV 缓存；**删除文档后利用缓存快速重建受影响实体/关系**（automatic KG regeneration），配套 `kg_integrity_repair.py`（审计 full_entities/full_relations 恢复锚点）与 `source_conflict_repair.py`（修复重复 source key）；查询缓存按"问题+模式+模型参数"为 key |
| 已知坑 | 文件级抽取遇到 LLM 超时会整文件失败（[Issue #2271](https://github.com/HKUDS/LightRAG/issues/2271)）；删除文档后关联 chunk 清理不干净（[Issue #2442](https://github.com/HKUDS/LightRAG/issues/2442)） |
| 存储 | KV/向量/图/文档状态四类存储可插拔：图支持 Neo4j / Memgraph，Postgres 可一站式承担四类 |

**对 Mnemos 可借鉴**

1. **增量重建而非全量重建**：LightRAG 的"删一个文档 → 用抽取缓存重建受影响子图"正是 Mnemos 该从"按 knowledge_id 清空重建"升级的方向。
2. **缓存 key 设计**：抽取缓存天然应以"chunk 内容 hash + 模型 + prompt 版本"为 key；schema 变更时把 schema 版本也纳入 key，即可做到"内容没变且 schema 没变 → 跳过"。
3. **source_id 上限与降级策略**：证据列表要做上限控制（Mnemos 可复用其思路，但上限应放在 Postgres 侧 graph_facts.evidence，避免 Neo4j 边属性膨胀）。
4. **完整性修复工具**：`kg_integrity_repair` 的"锚点审计"思想值得抄——投影层（Neo4j）与事实层（Postgres）之间跑定期对账任务。
5. **教训**：文件级失败粒度太大 → Mnemos 抽取按 chunk 粒度入任务队列，失败只重试单 chunk。

来源：[HKUDS/LightRAG](https://github.com/HKUDS/LightRAG) | [Issue #1631](https://github.com/HKUDS/LightRAG/issues/1631) | [Issue #2271](https://github.com/HKUDS/LightRAG/issues/2271) | [Issue #2442](https://github.com/HKUDS/LightRAG/issues/2442)

---

### 1.4 RAGFlow（infiniflow）

- **定位一句话**：深度文档理解 + 模板化分块的 RAG 引擎，其知识图谱功能是"在通用 RAG 产品里开箱即用地集成 GraphRAG/LightRAG"的范例。
- **许可证**：Apache-2.0。
- **活跃度**：93,233 stars（本项目调研中最高），最近提交 2026-10-31，极活跃。

**核心机制**（来源：[infiniflow/ragflow](https://github.com/infiniflow/ragflow)、[RAGFlow 知识图谱实践（hustyichi）](https://hustyichi.github.io/2025/04/02/ragflow-kg)、[知乎：学习 RAGFlow 的知识图谱功能](https://zhuanlan.zhihu.com/p/1934909298326114420)）

| 维度 | 做法 |
|---|---|
| 分块 | 模板化分块（general/paper/book/laws/QA/table 等多套模板），分块可视化并允许人工干预——"可解释的智能分块" |
| 图谱配置 | 知识库级开关：`entity_types`（默认 organization/person/geo/event/category，可自定义）、`method`（light=LightRAG / general=GraphRAG 两套实现）、**实体消解（entity resolution，LLM 把指向同一实体的不同名称合并）**、**社区发现 + 社区报告生成开关**、page rank；多轮抽取补漏 |
| 引用 | 全链路 grounded citations，回答可追溯到 chunk |
| 新方向 | 2026-08 Knowledge Compilation：文档/数据集级自动生成 Wiki、Graph、Tree、PageIndex、脑图、时间线、Skills 等多种知识形态 |

**对 Mnemos 可借鉴**

1. **把图谱当知识库的一个可开关"增强层"**：不改变既有向量检索主链路，图谱失败不阻塞入库——B 期管线应保持这种解耦。
2. **实体消解作为独立开关与独立步骤**（LLM 判定"这两个名字是同一个实体"）：与"抽取"解耦，便于单独审核与重跑；AMBIGUOUS 实体进人工审核队列。
3. **两档抽取方法**（light/general）：Mnemos 可提供"快速模式（只抽类型+关系粗边）"与"标准模式（带证据对齐与多轮补抽）"，对应不同成本档位。
4. **默认实体类型集**（organization/person/geo/event/category）可作为 Mnemos 各领域 schema 模板的起点。

来源：[infiniflow/ragflow](https://github.com/infiniflow/ragflow) | [hustyichi: RAGFlow 知识图谱实践](https://hustyichi.github.io/2025/04/02/ragflow-kg) | [知乎解析](https://zhuanlan.zhihu.com/p/1934909298326114420) | [官方文档站](https://ragflow.io/docs/dev/)

---

### 1.5 Graphiti（Zep）★ 新发现，重点参考

- **定位一句话**：为 Agent 构建实时时序知识图谱——事实带有效时间窗、episode 全程溯源、增量即时写入不做全量重算。
- **许可证**：Apache-2.0。
- **活跃度**：32,378 stars，最近提交 2026-10-28，非常活跃。

**核心机制**（来源：[getzep/graphiti README](https://github.com/getzep/graphiti)、[graphiti_core/edges.py 源码](https://github.com/getzep/graphiti/blob/main/graphiti_core/edges.py)）

| 维度 | 做法 |
|---|---|
| 双时间线 | 每条边（事实）字段（源码核实）：`fact`（自然语言事实句）、`valid_at`（事实开始为真）、`invalid_at`（事实被新事实推翻的时间）、`expired_at`（事实自然过期时间）、`reference_time`（来源 episode 的参考时间）、`episodes`（引用该边的 episode id 列表）、`attributes`、`fact_embedding`；支持"查询现在为真的事实"与"查询过去某时点的事实" |
| 溯源 | **Episode（原始摄入数据）是所有派生事实的溯源锚点**——每个 fact 可回溯到产生它的 episode |
| 节点模型 | Entity 节点（可演化的 summary）+ Episodic 节点（原始数据）+ Community 节点 |
| 实体消歧 | LLM 结构化输出抽取 + 去重，可配决策模型路由 |
| 增量 | 新 episode 即时融入图，"无需批量重算"；冲突事实自动失效（invalidate 而非删除），时间历史保留 |
| 本体 | 支持预设本体（Pydantic 定义实体/边类型）或从数据学习 |
| 检索 | 语义嵌入 + BM25 + 图遍历混合检索，查询时无需 LLM 汇总，亚秒级 |
| 局限 | **无显式快照/回滚/分支能力**（只有时间历史）；官方 README 明示其参考 MCP/REST 服务无鉴权，不可直接生产使用 |

**对 Mnemos 可借鉴**

1. **边的字段设计直接抄**：`fact / valid_at / invalid_at / expired_at / reference_time / episodes[]`——比 GraphRAG 的 weight 更贴近"事实生命周期"，graph_facts 表照此设计（B 期可只落 `valid_at/invalid_at` 或干脆留到 C 期，但字段预留）。
2. **"失效而非删除"**：新事实推翻旧事实时旧行置 superseded，不物理删除——GraphCommit 的回滚因此才有历史可回放。
3. **Episode 溯源模式**：episode id 列表等价于 Mnemos 的 evidence（doc_id + chunk_id + locators），证明"每条边回溯到摄入单元"在生产级产品中是成立的。
4. **教训**：Graphiti 有时间历史但**没有 commit/回滚**——这正是 Mnemos GraphCommit 要自建的部分，也说明"版本化"在同类产品中是空白点、是我们的差异化能力。

来源：[getzep/graphiti](https://github.com/getzep/graphiti) | [edges.py](https://github.com/getzep/graphiti/blob/main/graphiti_core/edges.py) | [Graphiti 文档（help.getzep.com/graphiti）](https://help.getzep.com/graphiti)

---

### 1.6 TerminusDB ★ 新发现，GraphCommit 的直接参照

- **定位一句话**：自带 git 式版本控制的知识图谱数据库——commit 链、分支、diff、时间旅行查询。
- **许可证**：Apache-2.0。
- **活跃度**：3,464 stars，5,984 commits，最近提交 2026-10-31，持续活跃（v12 在增强时序推理与向量检索）。

**核心机制**（来源：[terminusdb/terminusdb README](https://github.com/terminusdb/terminusdb)）

| 维度 | 做法 |
|---|---|
| 版本控制 | 每次更新形成一个 commit，构成**不可变 commit 链**；支持 branch / merge / clone / push / pull（"git-for-data"）；任意历史状态可查询（time-travel） |
| Diff/Patch | 任意两个 commit 间做 JSON diff-and-patch |
| Schema | Schema 即文档类（document classes），可通过文档 API 插入，并强制 schema 约束保证数据质量 |
| 查询 | WOQL（Datalog 系查询语言，支持路径查询）+ REST + GraphQL + RDF 语义接口 |

**对 Mnemos 可借鉴**

1. **"commit = 不可变链上节点"的数据模型**：graph_commits 表设计（parent_commit_id / changes[] / root_hash）可直接参照；**回滚 = 生成指向父状态的 revert commit 并重投影**，绝不改写历史。
2. **Schema 也进 commit**：TerminusDB 的 schema 与数据同链版本化——Mnemos 的 schema 发布与 GraphCommit 应互相引用版本号（commit 记录当时 schema_version），保证"这份图是用哪版 schema 抽的"可审计。
3. **Diff/Patch 是灰度评审的基础**：approve 前 UI 展示的就是 changes diff，TerminusDB 证明了这在图数据上完全可行。
4. **不整体迁移的理由**：TerminusDB 是独立数据库（Rust/Datalog 栈），引入即替换存储层，与现有 Neo4j + Postgres 冲突；只借鉴其版本控制语义，用 Postgres 实现即可。

来源：[terminusdb/terminusdb](https://github.com/terminusdb/terminusdb) | [TerminusDB 文档](https://docs.terminusdb.com/)

---

### 1.7 neo4j neosemantics（n10s）

- **定位一句话**：让 Neo4j 读写 RDF 世界（OWL/RDFS/SKOS）并做 SHACL 校验的官方 Labs 插件——"本体标准如何映射到属性图"的权威参考。
- **许可证**：Apache-2.0。
- **活跃度**：971 stars，最近提交 2026-10-09；Neo4j Labs 项目（非官方产品级支持），**不适用于 Aura 云服务**。

**核心机制**（来源：[neo4j-labs/neosemantics](https://github.com/neo4j-labs/neosemantics)、[Neo4j Labs 页](https://neo4j.com/labs/neosemantics/)）

| 维度 | 做法 |
|---|---|
| RDF 互转 | Turtle / N-Triples / JSON-LD / RDF/XML 等格式导入导出，含 RDF* 变体 |
| 本体导入 | `n10s.onto.import.fetch`（OWL/RDFS/SKOS 本体导入，比通用 rdf.import 更高层的语义处理）；导入前需 `n10s.graphconfig.init()` 配置，`handleVocabUris: MAP/SHORTEN/IGNORE` 决定词表 URI 如何映射为 Neo4j 标签/属性名 |
| 校验与推理 | SHACL 图校验；基础推理（inferencing） |
| 限制 | 仅限自托管 Neo4j；OWL 复杂公理（如基数限制、传递性）映射到 LPG 会丢语义（社区 issue 中多次出现导入生成"错误图"的问题，如 [Issue #324](https://github.com/neo4j-labs/neosemantics/issues/324)） |

**对 Mnemos 可借鉴**

1. **术语对齐表**（最重要）：n10s 证明了 OWL/RDFS/SKOS 概念向 LPG 的映射惯例——Class→Node label、rdfs:subClassOf→继承关系、SKOS 三类标签→名称体系。Mnemos 的 ontology 模型应对齐这套惯例，保证未来能导出标准本体。
2. **SKOS 别名体系直接采纳**：`prefLabel`（首选名）/`altLabel`（别名）/`hiddenLabel`（不参与检索但保留的拼写）三者两两互斥（来源：[W3C SKOS Reference](https://www.w3.org/TR/skos-reference/)）——对应 ontology_types 的 display_name / aliases / hidden_aliases 三字段，实体归一时用 altLabel 匹配、归一结果写回别名表。
3. **SHACL 思想**：schema 约束校验不必用 RDF，但"约束与数据分离、发布前校验"的模型照搬——即 KnowAxis 的兼容性检查。
4. **不引入插件本身**：见 §3 不采纳清单。

来源：[neo4j-labs/neosemantics](https://github.com/neo4j-labs/neosemantics) | [Neo4j Labs: neosemantics](https://neo4j.com/labs/neosemantics/) | [Issue #324](https://github.com/neo4j-labs/neosemantics/issues/324)

---

### 1.8 DB-GPT（eosphoros-ai）

- **定位一句话**：以 AWEL 工作流编排一切的企业级 AI 数据助手，其知识图谱模块是"三元组抽取 → 图存储 → 子图召回"最小闭环的简洁实现。
- **许可证**：MIT。
- **活跃度**：20,225 stars，最近提交 2026-10-31，活跃。

**核心机制**（来源：[DB-GPT Graph RAG 设计解读（蚂蚁范志东，源码级）](https://www.cnblogs.com/fanzhidongyzby/p/18252630/graphrag)、[官方 Graph RAG 文档](http://docs.dbgpt.cn/docs/v0.8.0/installation/integrations/graph_rag_install)）

| 维度 | 做法 |
|---|---|
| 抽取 | `KnowledgeGraphBase.aload_document(chunks)`：每 chunk 经 `TripletExtractor`（few-shot 引导抽 (subject, predicate, object)，LlamaIndex 风格）→ 逐条 `graph_store.insert_triplet` |
| 写入 | TuGraph 的 `insert_triplet` 将三元组翻译为 3 条 Cypher MERGE（起点节点 / 终点节点 / 边） |
| 检索 | `asimilar_search_with_scores`：先关键词抽取，再图存储 `explore(subs, direct, depth, fan, limit)` 变长路径召回子图，**子图以 Graph 对象直接进 prompt**（避免 Path/Table 编码浪费 token） |
| 本体 | 内置 BuiltinKnowledgeGraph 无 schema 约束（纯 OpenIE 式）；OpenSPG 对接在推进中 |
| 质量 | 文章建议方向：持续优化提示词 + 引入专有抽取微调模型（如 OneKE） |

**对 Mnemos 可借鉴**

1. **`fan`（扇出限制）参数**：explore 时限制单点扇出防数据热点——Mnemos 的图谱检索（graph_search）应加同款参数，避免热门实体把上下文撑爆。
2. **子图 → prompt 的 Graph 对象编码**：与 Mnemos 现有 graph_truncation 机制一致，可参考其"关键词 → 种子节点 → 定向子图"两步式召回。
3. **教训**：无 schema 约束的三元组抽取正是 KnowAxis 定义中要避免的"OpenIE 噪声"形态——反例确认了 schema 约束的必要性。

来源：[eosphoros-ai/DB-GPT](https://github.com/eosphoros-ai/DB-GPT) | [Graph RAG 设计解读（cnblogs）](https://www.cnblogs.com/fanzhidongyzby/p/18252630/graphrag) | [官方文档](http://docs.dbgpt.cn/docs/v0.8.0/installation/integrations/graph_rag_install)

---

### 1.9 HippoRAG / HippoRAG 2（OSU NLP Group）

- **定位一句话**：模拟海马体记忆索引的学术框架——OpenIE 建图 + Personalized PageRank 单步多跳检索，索引成本远低于 GraphRAG。
- **许可证**：MIT。
- **活跃度**：4,085 stars，最近提交 2026-10-06（NeurIPS'24，续作 HippoRAG 2 为 ICML'25）。

**核心机制**（来源：[OSU-NLP-Group/HippoRAG](https://github.com/OSU-NLP-Group/HippoRAG)）

| 维度 | 做法 |
|---|---|
| 建图 | LLM 对段落做 OpenIE：命名实体 + (subject, relation, object) 三元组 → 图节点/边；实体与事实嵌入与段落嵌入同存 |
| 检索 | 查询识别出的实体节点作为种子，跑一次 Personalized PageRank 激活扩散，一步完成多跳——不需要迭代式检索链，也不建社区摘要（与 GraphRAG 的关键差异） |
| 增量 | 新文档直接索引进现有图（MCP index 工具跳过已存在段落，delete 可移除） |
| 效果 | HippoRAG 2 在多跳与长上下文基准上超过 GraphRAG / RAPTOR / LightRAG，索引资源消耗更低 |

**对 Mnemos 可借鉴**

1. **PPR 用于"按实体探索 / 专家发现"**：影响分析、专家发现等图应用不需要 LLM，一次 PPR 就能给出"与该实体最相关的实体/文档"排序，Neo4j 内置 GDS 库即可实现，B 期图应用的性价比之王。
2. **扁平图谱 + 种子扩散**：说明 B 期不必先做社区检测也能支撑高质量检索。

来源：[OSU-NLP-Group/HippoRAG](https://github.com/OSU-NLP-Group/HippoRAG) | [HippoRAG 论文 arXiv:2405.14831](https://arxiv.org/abs/2405.14831)

---

### 1.10 fast-graphrag（circlemind-ai）

- **定位一句话**：HippoRAG 路线的工程化封装——领域提示词 + 实体类型清单驱动的可定制 GraphRAG。
- **许可证**：MIT；3,967 stars，最近提交 2025-11-01（活跃度明显下降，一年未提交）。

**核心机制与可借鉴**（来源：[circlemind-ai/fast-graphrag](https://github.com/circlemind-ai/fast-graphrag)）

1. **"domain + example queries + entity types"三要素声明式定义图谱**：用户用一段领域描述 + 期望实体类型就让框架自动建图——与 KnowAxis"自然语言引导建模"入口同构，验证了该交互的可行性。
2. `with_references=True` 让回答带引用；checkpointing 防数据损坏。
3. **不作为依赖引入**：Python 工作目录持久化模型、活跃度下滑、功能与 LightRAG/GraphRAG 重合。

---

### 1.11 kotaemon（Cinnamon）

- **定位一句话**：带 GraphRAG 集成与图谱可视化的开源 RAG 问答 UI。
- **许可证**：Apache-2.0；25,841 stars，最近提交 2026-07-14（维护放缓）。

（来源：[Cinnamon/kotaemon](https://github.com/Cinnamon/kotaemon)）对 Mnemos 的价值主要在**产品层**：图谱探索视图与证据引用的 UI 形态（节点点击 → 原文引用回跳）可作前端参考；不引入其代码。

---

### 1.12 标准与工具链：OWL 2 / SKOS / SHACL / RDFLib / Apache Jena

这部分不是"引入依赖"而是"术语表对齐"，保证 Mnemos 自研本体模型与业界语义互认。

| 标准/工具 | 与 Mnemos 的对应关系 | 来源 |
|---|---|---|
| **SKOS**（W3C 2009 推荐） | 概念方案（ConceptScheme）模型；**prefLabel / altLabel / hiddenLabel 三者两两互斥**——别名设计的规范依据：display_name（唯一首选名）/ aliases（参与检索匹配）/ hidden_aliases（仅记录不参与匹配）；`skos:broader/narrower` → 类型继承；`skos:related` → 类型关联 | [W3C SKOS Reference](https://www.w3.org/TR/skos-reference/)、[SKOS Primer](https://www.w3.org/TR/skos-primer/) |
| **OWL 2**（W3C 推荐） | 类/属性/数据类型/基数约束的形式化体系——Mnemos ontology_properties 的 data_type / required / enum / cardinality 字段语义应对齐 OWL 2 命名（便于未来导出） | [OWL 2 Overview](https://www.w3.org/TR/owl2-overview/) |
| **SHACL**（W3C 推荐） | 约束与数据分离的校验语言——schema_proposals 兼容性检查的规则模型参照 | [W3C SHACL](https://www.w3.org/TR/shacl/) |
| **RDFLib**（Python，BSD-3，2,542 stars） | 若未来做 RDF 导出/导入适配层的工具选择；当前 B 期不需要 | [RDFLib/rdflib](https://github.com/RDFLib/rdflib) |
| **Apache Jena**（Java，Apache-2.0，1,491 stars） | 语义网全家桶（RDF API / SPARQL ARQ / 推理机 / TDB2 / Fuseki），Go 栈不引入；其推理机"规则前向传播"对应 INFERRED 边生成思想 | [apache/jena](https://github.com/apache/jena) |

**关键结论**：B 期用 LPG + Postgres 自研本体模型是对的，但**字段命名与语义应向 OWL 2 / SKOS 对齐**（尤其别名三件套），保留未来"导出标准本体"的出口，避免语义被产品逻辑锁死。

---

### 1.13 抽取模型补充：DeepKE / OneKE（zjunlp）

- **DeepKE**：EMNLP 2022 的知识抽取工具集，支持 schema 指导的实体/关系/属性抽取，4,488 stars，MIT，仍在维护（来源：[zjunlp/DeepKE](https://github.com/zjunlp/DeepKE)）。
- **OneKE**：WWW 2025 的 Docker 化 schema 引导 LLM Agent 抽取系统（197 stars，MIT；同系 DeepKE 生态），DB-GPT 社区也推荐其作为专有抽取器（来源：[zjunlp/OneKE](https://github.com/zjunlp/OneKE)）。
- **对 Mnemos**：B 期先用通用 LLM + schema 注入 prompt；当通用模型抽取质量不足时，把 DeepKE/OneKE 作为"可替换的抽取后端"接入（Mnemos 抽取服务本就面向 LLM 适配层设计，替换成本低）。

---

## 2. 横向对比

| 项目 | Schema 约束 | 实体归一 | 证据绑定 | 置信度 | 增量/缓存 | 版本化/回滚 | 许可证 | Stars | 最近提交 |
|---|---|---|---|---|---|---|---|---|---|
| OpenSPG/KAG | ★★★（SPG 三模型 + KGDSL） | ★★★（归一算子） | △（互索引） | ✗ | △（append 式属性更新） | ✗ | Apache-2.0 | 9.1k / 2.3k | 2026-01 / 2025-07 |
| Microsoft GraphRAG | △（entity_types 枚举） | △（LLM 描述合并） | ★（text_unit_ids） | △（weight/degree） | ✗ | ✗ | MIT | 36.5k | 2026-10-29 |
| LightRAG | △（可自定义实体类型） | ★（merge 阶段 + 修复工具） | ★（source_id + 上限） | ✗ | ★（抽取 KV 缓存 + 增删重建） | ✗ | MIT | 40.4k | 2026-10-29 |
| RAGFlow | △（entity_types + 模板） | ★（entity resolution 开关） | ★（grounded citations） | ✗ | △ | ✗ | Apache-2.0 | 93.2k | 2026-10-31 |
| Graphiti | ★（Pydantic 预设本体/学习式） | ★（LLM 去重路由） | ★（episodes 溯源） | ✗（时间窗替代） | ★（实时增量） | △（时间历史，无回滚） | Apache-2.0 | 32.4k | 2026-10-28 |
| TerminusDB | ★（schema 即文档类） | —（数据库层不做） | — | — | — | ★★★（commit 链/分支/时间旅行） | Apache-2.0 | 3.5k | 2026-10-31 |
| neosemantics | ★（OWL/SKOS/SHACL） | ✗ | ✗ | ✗ | ✗ | ✗ | Apache-2.0 | 1.0k | 2026-10-09 |
| DB-GPT | ✗（纯 OpenIE 式） | ✗ | △（chunk 级计数） | ✗ | ✗ | ✗ | MIT | 20.2k | 2026-10-31 |
| HippoRAG(2) | ✗ | △ | ★（段落绑定） | ✗ | ★（图上增量索引） | ✗ | MIT | 4.1k | 2026-10-06 |
| fast-graphrag | △（domain+types 声明） | △ | ★（with_references） | ✗ | △ | △（checkpoint） | MIT | 4.0k | 2025-11-01 |

> 结论：**没有任何一个开源项目同时具备"schema 约束 + 证据绑定 + 三级置信度 + commit 版本化"四件套**。Graphiti 差在无版本化，TerminusDB 差在无抽取管线，KAG 差在无置信度与版本化、活跃度放缓。KnowAxis V3.0 §8-9 的设计组合（SchemaProposal + 三级置信度 + GraphCommit）在开源界是空白，各项目机制拼起来恰好覆盖：KAG（schema 建模/约束抽取）+ GraphRAG/LightRAG（管线与缓存）+ Graphiti（字段与溯源）+ TerminusDB（commit 语义）。

---

## 3. Mnemos B 期采纳建议

### 3.1 存储总原则：Postgres 是事实层（system of record），Neo4j 是投影层

- **权威事实（实体登记、事实行、证据、置信度、版本）全部存 Postgres**；Neo4j 只存可重投影的图结构（节点、边、检索属性）。
- 理由：(a) GraphCommit 的回滚/重放需要 append-only 的权威存储（TerminusDB 经验）；(b) 图结构丢失可从 Postgres 全量重投影（LightRAG 的 integrity repair 思想）；(c) 现有代码已经把 Neo4j 当"可清空重建"的检索层用，改造成本最低。
- Neo4j 侧证据字段只放"指向 Postgres 的 id 引用 + 轻量置信度"，避免大 JSON 边属性拖慢图遍历。

### 3.2 Postgres 侧表草案

以下为字段级建议（GORM 建表，命名对齐 KnowAxis 术语；`_` 前缀表示可后置到 C 期）：

**ontology_schemas（本体 schema 版本表，发布即不可变）**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | uuid pk | |
| kb_id / tenant_id | | 作用域（沿用现有租户模型） |
| name | text | schema 名称 |
| version | int | 递增版本号（同 name 下唯一） |
| parent_version_id | uuid | 上一版本（版本树） |
| status | enum | draft / published / deprecated |
| spec | jsonb | **发布时完整快照**（canonical JSON + checksum），保证历史审计不被后续编辑污染 |
| checksum | text | canonical spec hash |
| created_by / published_by / published_at | | |

**ontology_types（类型表）**

| 字段 | 说明 |
|---|---|
| id, schema_id | 隶属 schema 版本 |
| key | 机器名（如 Equipment），schema 内唯一 |
| display_name | 首选名（SKOS prefLabel 对齐） |
| aliases | jsonb，别名数组（SKOS altLabel；参与检索匹配） |
| hidden_aliases | jsonb（SKOS hiddenLabel；仅记录不匹配，可后置） |
| parent_type_id | 继承（subClassOf） |
| kind | entity / event / concept |
| constraints | jsonb：required 属性清单、枚举约束等（SHACL 思想） |
| examples | jsonb，few-shot 例（同时喂给抽取 prompt 与自动建模 prompt） |
| description | 定义（进抽取 prompt） |

**ontology_properties（属性表）**：id, schema_id, type_id, key, data_type（对齐 OWL 2：string/number/date/enum/ref）, required bool, multi_value bool, enum_values jsonb, unit, description。

**ontology_relations（关系表）**：id, schema_id, key, display_name, source_type_id, target_type_id（两端类型约束 = KAG 谓词模型）, cardinality（1-1/1-n/n-n）, symmetric bool, properties jsonb（边允许的属性）, description。

**schema_proposals（AI 建模提案）**

| 字段 | 说明 |
|---|---|
| id, kb_id, base_schema_id | 基于哪个版本提案 |
| source | ai / user |
| motivation | 为什么（AI 生成：哪些文档出现当前 schema 无法表达的概念） |
| changes | jsonb：结构化 diff（add_type / add_property / add_relation / add_alias / modify_constraints…） |
| evidence_samples | jsonb：触发提案的文档/chunk 样例（带 SourceLocators，供评审回看原文） |
| compatibility_report | jsonb：兼容性检查结果（breaking 变更清单、受影响实体/事实预估数量、迁移方案） |
| status | proposed / approved / rejected / merged |
| reviewer, decided_at, resulting_version_id | 决策链 |

**graph_commits（图谱变更集，不可变）**

| 字段 | 说明 |
|---|---|
| id, kb_id | |
| parent_commit_id | commit 链（TerminusDB 模型） |
| schema_id + schema_version | **提交时点所用 schema**（审计"这份图用哪版 schema 抽的"） |
| kind | incremental / full_rebuild |
| changes | jsonb：原子操作列表，每条 {op: upsert_entity / upsert_fact / supersede_fact / retire_entity, payload, old_hash} |
| pre_root_hash / post_root_hash | 应用前后事实集 Merkle 根（重放校验用，见 §3.4） |
| status | pending / approved / applied / rolled_back（rolled_back 仅为标记，历史不删） |
| gray_config | jsonb：灰度范围（kb 白名单 / 文档范围 / 流量比例） |
| created_by / approved_by / applied_at | |

**graph_entities（实体登记表）**

| 字段 | 说明 |
|---|---|
| id, kb_id, commit_id | 引入该实体版本的 commit |
| type_key | 须命中已发布 ontology_types（违反者只能进 AMBIGUOUS 桶） |
| canonical_name / aliases | 归一名 + 别名（SKOS 对齐） |
| sig_hash | **归一签名 hash**（小写化、去停用词、别名折叠后的哈希）——实体归一的主键级依据 + 增量判断依据 |
| props | jsonb（schema 校验过的属性） |
| confidence_level | EXTRACTED / INFERRED / AMBIGUOUS；confidence_score float |
| status | active / merged / retired；merged_into_id |
| evidence | jsonb：[{doc_id, chunk_id, source_locators[], quote}]（**复用 internal/types/source_locator.go 的 SourceLocator，不造新结构**） |

**graph_facts（事实表，append-only）**

| 字段 | 说明 |
|---|---|
| id, kb_id, commit_id | |
| subject_entity_id / predicate / object_entity_id | predicate 必须命中 ontology_relations.key |
| props | jsonb（边属性） |
| fact | text（自然语言事实句，Graphiti `fact` 字段对齐，便于展示与嵌入） |
| confidence_level / confidence_score | 三级置信度；INFERRED 行另带 rule_id / derived_from_fact_id |
| status | active / superseded / retracted；superseded_by_id（失效而非删除，Graphiti 模式） |
| _valid_at / _invalid_at / _expired_at | 时间窗字段预留（Graphiti 对齐，B 期可只建列不启用逻辑） |
| evidence | jsonb，同 graph_entities |
| extraction_job_id / chunk_id | 抽取溯源 |

**chunk_extraction_cache（增量抽取缓存）**

| 字段 | 说明 |
|---|---|
| chunk_id, kb_id | |
| content_hash | chunk 内容 hash |
| schema_version + prompt_version + model | 组成缓存 key（LightRAG 经验 + schema 变更即失效） |
| result | jsonb（带 evidence 的抽取结果） |
| created_at / hit_count | |

### 3.3 Neo4j 侧设计建议

```
(:Entity {eid, kb_id, type, name, aliases[], confidence_level, schema_version, commit_id})
  - 索引：(kb_id, eid) 唯一；(kb_id, sig_hash) 用于归一匹配；(kb_id, name) 用于种子检索
  - 沿用现有 NameSpace 标签机制（kb 标签）做物理隔离，灰度期可另打 :Shadow 标签

[:REL {fid, kb_id, commit_id, confidence_level, confidence_score, status, _valid_at, _invalid_at}]
  - 边 type 直接用 ontology_relations.key（有 schema 保证的枚举，边类型不再自由生长）
  - fid 指向 Postgres graph_facts.id；证据只存 fid 回查，不把 evidence JSON 塞进边属性
  - supersede 时置 status='superseded' 并保留边（时间历史 + 回滚都需要），查询默认过滤 status='active'
```

- 现有 `apoc.merge.node / apoc.merge.relationship` 写入路径改造为"从 GraphCommit changes 重放"，APOLIC 语义不变但输入从"LLM 原始输出"变为"审批通过的变更集"。
- 全量重投影 SQL→Cypher 同步器作为兜底任务（对应 LightRAG `kg_integrity_repair` 思想）。

### 3.4 抽取管线改造建议

1. **Schema 约束注入（对齐 KAG/GraphRAG）**：抽取 prompt 注入已发布 schema 的类型定义 + 关系两端约束 + 每类型 few-shot 例；输出用 JSON Schema 强约束（type/relation 为枚举）。越界输出不丢弃——降级进 AMBIGUOUS 桶并可作为 SchemaProposal 的证据样例（这正好闭环了"AI 文档驱动自动建模"）。
2. **证据绑定复用 source locators**：要求 LLM 输出每条实体/关系附 `quote`（原文片段）；用 `internal/sourceloc` 现有对齐能力把 quote 对齐回 chunk 内文本，再连同 chunk 已有的 SourceLocators 写入 evidence。等于 GraphRAG 的 text_unit_ids 模式 + Graphiti 的 episodes 模式，但定位精度到"页/区块/字符区间"，优于两个参考项目。
3. **置信度三级判定**：EXTRACTED = schema 合法 + 证据对齐成功 + 分数达阈；INFERRED = 规则/推导产生（记 rule_id）；AMBIGUOUS = 类型越界、实体消歧冲突、多来源矛盾、低分——进人工审核队列（RAGFlow entity resolution + 人工干预分块的交互思想）。
4. **实体归一两级**：先 sig_hash 精确匹配（LightRAG exact key 模式，便宜），冲突或近邻再做 LLM 判定（RAGFlow entity resolution 模式），合并结果写回 aliases。
5. **增量缓存**：`hash(chunk.content) + schema_version + prompt_version + model` 为 key，未命中才调 LLM（LightRAG KV 缓存模式）；schema 发布新版本时按变更 diff 计算受影响类型，只重抽涉及这些类型的 chunk（兼容性检查报告里已有影响面数据，直接复用）。
6. **chunk 粒度任务化**：吸取 LightRAG Issue #2271 教训，抽取任务以 chunk 为失败/重试粒度。

### 3.5 GraphCommit 版本化方案建议

参照 TerminusDB 的 commit 链 + LightRAG 的增量重建 + Graphiti 的失效模式：

1. **提交不可变**：graph_commits 一经 applied 不得 UPDATE 内容（含 changes jsonb）；纠错只能产生新 commit。facts/entities 行 append-only，废止用 status + superseded_by_id。
2. **重放（replay）**：apply = 把 changes 中的原子操作按序写入 Postgres 事实层 → 增量重投影受影响子图到 Neo4j。重投影是幂等的：任何投影状态都可以由"全量事实 + commit 链"重新推演（root_hash 断言校验）。
3. **回滚（rollback）**：生成 revert commit（changes 为目标 commit 的逆操作），指向 parent 状态重投影——不改写历史，审计链完整。回滚同样走审批与灰度。
4. **灰度**：commit.gray_config 控制生效范围；Neo4j 侧用 :Shadow 标签或独立 kb 命名空间做影子投影，验证指标（命中率、冲突数）达标后切换正式标签。
5. **回归**：apply 前跑 golden set 断言（固定查询集的子图形态 + 统计断言：实体数、各类型计数、关键路径存在性）+ pre/post root hash 校验；schema 发布与 GraphCommit 均纳入同一回归框架。
6. **对账任务**：定时比对 Neo4j 投影与 Postgres 事实层（计数 + hash 抽样），漂移即告警并自动重投影（LightRAG repair 工具思想的产品化）。

### 3.6 明确不采纳清单（及原因）

| 不采纳项 | 原因 |
|---|---|
| **引入 RDF/OWL 全家桶（Apache Jena、RDFLib、n10s 插件作为运行时依赖）** | 技术栈不匹配（Java/Python vs Go）；n10s 是 Labs 项目、不支持 Aura、OWL 复杂公理映射 LPG 会丢语义（Issue #324）；企业场景 LPG + 自研 schema 表已覆盖需求。**但采纳其术语体系**：SKOS 别名三件套、SHACL 校验思想、OWL 2 字段命名对齐，保留未来标准导出口 |
| **引入 OpenSPG/KAG 引擎作为运行时** | 部署重（Java 引擎 + Docker Compose 全家桶），与 WeKnora Go 栈冲突；openspg 引擎仓库自 2025-07 后提交稀少，作为核心依赖风险高。借鉴其 schema 三模型、约束抽取、归一算子、KGDSL 思想，用 Go 自实现受限版 |
| **社区检测 + 社区报告作为 B 期必做** | 面向"全局性摘要问答"，token 成本高（GraphRAG 官方文档确认图抽取已占 75% 成本，社区报告再叠加）；B 期图应用目标（实体探索/路径/影响分析/专家发现）用 PPR（HippoRAG 路线）+ 中心度即可支撑。社区检测列为 C 期可选 |
| **引入 LightRAG / GraphRAG / fast-graphrag 作为依赖库** | Python 栈不匹配；但 LightRAG 的缓存/增量重建/修复工具、GraphRAG 的 TextUnit 溯源与描述合并、fast-graphrag 的声明式建模入口均全盘吸收其思想 |
| **Neo4j 作为唯一事实源** | 无法支撑回放/重投影/审计（现有代码按 knowledge_id 清空重建的模式不可延续到企业场景）；事实层必须落 Postgres |
| **B 期做完整双时间线（valid_at/invalid_at 语义逻辑）** | Graphiti 证明了价值但也证明了复杂度（冲突判定、时间抽取是独立难题）；字段预留、逻辑后置，B 期专注 commit 版本化 |
| **引入 TerminusDB 替换存储** | 独立数据库即替换存储层；仅采纳其 commit/branch/diff 语义在 Postgres 实现 |
| **引用第三方 MCP/REST 图服务（Graphiti 参考服务）** | 官方明示无鉴权与提示注入防护，不可生产使用 |

---

## 4. 参考资料

**项目仓库**
- OpenSPG/KAG：https://github.com/OpenSPG/KAG
- OpenSPG 引擎：https://github.com/OpenSPG/openspg ｜ 主页：https://spg.openkg.cn/en-US ｜ 文档：https://openspg.yuque.com/ndx6g9/docs_en
- Microsoft GraphRAG：https://github.com/microsoft/graphrag ｜ Methods：https://microsoft.github.io/graphrag/index/methods/ ｜ Dataflow：https://microsoft.github.io/graphrag/index/default_dataflow/
- LightRAG：https://github.com/HKUDS/LightRAG ｜ Issue #1631：https://github.com/HKUDS/LightRAG/issues/1631 ｜ Issue #2271：https://github.com/HKUDS/LightRAG/issues/2271 ｜ Issue #2442：https://github.com/HKUDS/LightRAG/issues/2442
- RAGFlow：https://github.com/infiniflow/ragflow ｜ 官方文档：https://ragflow.io/docs/dev/
- Graphiti：https://github.com/getzep/graphiti ｜ edges.py：https://github.com/getzep/graphiti/blob/main/graphiti_core/edges.py ｜ 文档：https://help.getzep.com/graphiti
- TerminusDB：https://github.com/terminusdb/terminusdb ｜ 文档：https://docs.terminusdb.com/
- neosemantics：https://github.com/neo4j-labs/neosemantics ｜ Labs 页：https://neo4j.com/labs/neosemantics/ ｜ Issue #324：https://github.com/neo4j-labs/neosemantics/issues/324
- DB-GPT：https://github.com/eosphoros-ai/DB-GPT ｜ Graph RAG 文档：http://docs.dbgpt.cn/docs/v0.8.0/installation/integrations/graph_rag_install
- HippoRAG：https://github.com/OSU-NLP-Group/HippoRAG ｜ 论文：https://arxiv.org/abs/2405.14831
- fast-graphrag：https://github.com/circlemind-ai/fast-graphrag
- kotaemon：https://github.com/Cinnamon/kotaemon
- DeepKE：https://github.com/zjunlp/DeepKE ｜ OneKE：https://github.com/zjunlp/OneKE
- RDFLib：https://github.com/RDFLib/rdflib ｜ Apache Jena：https://github.com/apache/jena

**标准**
- W3C SKOS Reference：https://www.w3.org/TR/skos-reference/ ｜ SKOS Primer：https://www.w3.org/TR/skos-primer/
- OWL 2 Overview：https://www.w3.org/TR/owl2-overview/
- W3C SHACL：https://www.w3.org/TR/shacl/
- KAG 论文：https://arxiv.org/abs/2409.13731

**社区解析**
- DB-GPT Graph RAG 设计解读（蚂蚁范志东）：https://www.cnblogs.com/fanzhidongyzby/p/18252630/graphrag
- RAGFlow 知识图谱实践（hustyichi）：https://hustyichi.github.io/2025/04/02/ragflow-kg
- 学习 RAGFlow 的知识图谱功能（知乎）：https://zhuanlan.zhihu.com/p/1934909298326114420

**Mnemos 代码基线（本次调研核实）**
- `/home/xiedex/code/Mnemos/internal/types/extract_graph.go`（GraphNode/GraphRelation/GraphData）
- `/home/xiedex/code/Mnemos/internal/application/repository/retriever/neo4j/repository.go`（APOC merge 写入、按 knowledge_id 重建）
- `/home/xiedex/code/Mnemos/internal/types/source_locator.go` 与 `/home/xiedex/code/Mnemos/internal/sourceloc/`（证据定位既有资产）
