<template>
  <div class="governance-console">
    <header class="governance-console__header">
      <div class="governance-console__heading">
        <h2 class="governance-console__title">{{ t('governanceConsole.title') }}</h2>
        <p class="governance-console__desc">{{ t('governanceConsole.description') }}</p>
      </div>
      <t-button variant="outline" size="medium" :loading="loading" @click="reloadActiveTab">
        <template #icon><t-icon name="refresh" /></template>
        {{ t('governanceConsole.refresh') }}
      </t-button>
    </header>

    <!-- 403 / 加载失败：整个治理面是 Admin+ 的入口 -->
    <div v-if="forbidden" class="governance-console__state">
      <t-empty :description="t('governanceConsole.noPermission')" />
    </div>

    <t-tabs v-else v-model="activeTab" class="governance-console__tabs" @change="onTabChange">
      <!-- ================= 审批（§6.2 十四类） ================= -->
      <t-tab-panel value="approvals" :label="t('governanceConsole.tabApprovals')">
        <div class="governance-console__toolbar">
          <t-select
            v-model="subjectFilter"
            class="governance-console__filter"
            clearable
            :placeholder="t('governanceConsole.columnType')"
            :options="subjectOptions"
            @change="loadApprovals"
          />
        </div>
        <t-table
          row-key="id"
          :data="approvals"
          :columns="approvalColumns"
          size="medium"
          hover
          :loading="loading"
          :empty="t('governanceConsole.emptyApprovals')"
        >
          <template #subject="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.title || row.subject_id }}</span>
              <span class="governance-subject__meta">
                {{ subjectLabel(row.subject_type) }} · {{ row.subject_id }}
              </span>
            </div>
          </template>
          <template #risk_level="{ row }">
            <t-tag size="small" variant="light" :theme="riskMeta(row.risk_level).theme">
              {{ riskMeta(row.risk_level).label }}
            </t-tag>
          </template>
          <template #sla_due="{ row }">
            <span :class="{ 'is-overdue': isOverdue(row) }">{{ formatTime(row.sla_due) }}</span>
          </template>
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="statusMeta(row.status).theme">
              {{ statusMeta(row.status).label }}
            </t-tag>
          </template>
          <template #op="{ row }">
            <div v-if="row.status === 'pending'" class="governance-actions">
              <t-button theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="decide(row, 'approved')">
                {{ t('governanceConsole.approve') }}
              </t-button>
              <t-button theme="danger" variant="text" size="small"
                :loading="busyId === row.id" @click="decide(row, 'rejected')">
                {{ t('governanceConsole.reject') }}
              </t-button>
            </div>
            <span v-else class="governance-actions__none">--</span>
          </template>
        </t-table>
      </t-tab-panel>

      <!-- ================= 官方答案（§13.7） ================= -->
      <t-tab-panel value="answers" :label="t('governanceConsole.tabAnswers')">
        <t-table
          row-key="id"
          :data="answers"
          :columns="answerColumns"
          size="medium"
          hover
          :loading="loading"
          :empty="t('governanceConsole.emptyAnswers')"
        >
          <template #question="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.question }}</span>
              <span class="governance-subject__meta">v{{ row.version || 0 }}</span>
            </div>
          </template>
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="answerStatusMeta(row.status).theme">
              {{ answerStatusMeta(row.status).label }}
            </t-tag>
          </template>
          <template #expires_at="{ row }">{{ formatDate(row.expires_at) }}</template>
          <template #op="{ row }">
            <div class="governance-actions">
              <t-button v-if="row.status === 'draft'" theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="submitAnswer(row)">
                {{ t('governanceConsole.submit') }}
              </t-button>
              <t-button v-if="row.status === 'in_review'" theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="approveAnswer(row)">
                {{ t('governanceConsole.approve') }}
              </t-button>
              <t-button v-if="row.status === 'approved'" theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="publishAnswer(row)">
                {{ t('governanceConsole.publish') }}
              </t-button>
              <t-button v-if="isAnswerLive(row)" theme="danger" variant="text" size="small"
                :loading="busyId === row.id" @click="retireAnswer(row)">
                {{ t('governanceConsole.retire') }}
              </t-button>
            </div>
          </template>
        </t-table>
      </t-tab-panel>

      <!-- ================= 模板中心（§3.2/§4.7） ================= -->
      <t-tab-panel value="templates" :label="t('governanceConsole.tabTemplates')">
        <div class="governance-console__split">
          <div class="governance-console__pane">
            <t-table
              row-key="id"
              :data="templates"
              :columns="templateColumns"
              size="medium"
              hover
              :loading="loading"
              :empty="t('governanceConsole.emptyTemplates')"
            >
              <template #fieldCount="{ row }">{{ row.fields?.length || 0 }}</template>
            </t-table>
          </div>
          <div class="governance-console__pane">
            <h3 class="governance-console__section">
              {{ t('governanceConsole.migrationPending') }}
            </h3>
            <t-table
              row-key="id"
              :data="migrations"
              :columns="migrationColumns"
              size="small"
              :loading="loading"
            >
              <template #op="{ row }">
                <div class="governance-actions">
                  <t-button theme="primary" variant="text" size="small"
                    :loading="busyId === row.id" @click="resolveMigration(row, 'done')">
                    {{ t('governanceConsole.migrationDone') }}
                  </t-button>
                  <t-button variant="text" size="small"
                    :loading="busyId === row.id" @click="resolveMigration(row, 'waived')">
                    {{ t('governanceConsole.migrationWaived') }}
                  </t-button>
                </div>
              </template>
            </t-table>
          </div>
        </div>
      </t-tab-panel>

      <!-- ================= 目录权限（§2.3） ================= -->
      <t-tab-panel value="permissions" :label="t('governanceConsole.tabPermissions')">
        <div class="governance-console__toolbar">
          <t-input
            v-model="permissionKbId"
            class="governance-console__filter"
            :placeholder="'knowledge_base_id'"
            @enter="loadPermissions"
          />
          <t-button size="medium" :loading="loading" @click="loadPermissions">
            {{ t('governanceConsole.refresh') }}
          </t-button>
        </div>
        <div class="governance-console__split">
          <div class="governance-console__pane">
            <t-table
              row-key="id"
              :data="folderGrants"
              :columns="grantColumns"
              size="small"
              :loading="loading"
            >
              <template #effect="{ row }">
                <t-tag size="small" variant="light"
                  :theme="row.effect === 'deny' ? 'danger' : 'success'">
                  {{ row.effect === 'deny'
                    ? t('governanceConsole.deny')
                    : t('governanceConsole.allow') }}
                </t-tag>
              </template>
              <template #op="{ row }">
                <t-button theme="danger" variant="text" size="small"
                  :loading="busyId === row.id" @click="removeGrant(row)">
                  {{ t('governanceConsole.delete') }}
                </t-button>
              </template>
            </t-table>
          </div>
          <div class="governance-console__pane">
            <h3 class="governance-console__section">
              {{ t('governanceConsole.ceilingViolations') }}
            </h3>
            <t-table
              row-key="id"
              :data="violations"
              :columns="violationColumns"
              size="small"
              :loading="loading"
            >
              <template #detected_at="{ row }">{{ formatTime(row.detected_at) }}</template>
            </t-table>
          </div>
        </div>

        <!-- 授权主体组（§2.1）：文档 ACL 可按组/角色授权，不必逐人维护。 -->
        <h3 class="governance-console__section governance-console__section--spaced">
          {{ t('governanceConsole.groupsTitle') }}
        </h3>
        <p class="governance-hint">{{ t('governanceConsole.groupsHint') }}</p>
        <div class="governance-toolbar">
          <t-button theme="primary" size="small" @click="openGroupDialog()">
            {{ t('governanceConsole.groupAdd') }}
          </t-button>
        </div>
        <t-table
          row-key="id"
          :data="aclGroups"
          :columns="groupColumns"
          size="small"
          :loading="loading"
          :empty="t('governanceConsole.emptyGroups')"
        >
          <template #group="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.name }}</span>
              <span class="governance-subject__meta">{{ row.description }}</span>
            </div>
          </template>
          <template #op="{ row }">
            <div class="governance-actions">
              <t-button variant="text" size="small" @click="openMembers(row)">
                {{ t('governanceConsole.groupMembers') }}
              </t-button>
              <t-button variant="text" size="small" @click="openGroupDialog(row)">
                {{ t('governanceConsole.feedEdit') }}
              </t-button>
              <t-popconfirm :content="t('governanceConsole.groupDeleteConfirm')"
                @confirm="removeGroup(row)">
                <t-button theme="danger" variant="text" size="small"
                  :loading="busyId === row.id">
                  {{ t('governanceConsole.feedDelete') }}
                </t-button>
              </t-popconfirm>
            </div>
          </template>
        </t-table>
      </t-tab-panel>

      <!-- ================= 搜索质量（§12.5） ================= -->
      <t-tab-panel value="searchQuality" :label="t('governanceConsole.tabSearchQuality')">
        <div class="governance-console__toolbar">
          <t-input-number
            v-model="windowDays"
            theme="normal"
            :min="1"
            :max="365"
            :label="t('governanceConsole.windowDays')"
            @change="loadSearchQuality"
          />
        </div>
        <div class="governance-metrics">
          <div class="governance-metric">
            <span class="governance-metric__label">{{ t('governanceConsole.totalQueries') }}</span>
            <span class="governance-metric__value">{{ quality.total_queries }}</span>
          </div>
          <div class="governance-metric">
            <span class="governance-metric__label">{{ t('governanceConsole.zeroResultQueries') }}</span>
            <span class="governance-metric__value">{{ quality.zero_result_queries }}</span>
          </div>
          <div class="governance-metric">
            <span class="governance-metric__label">{{ t('governanceConsole.zeroResultRate') }}</span>
            <span class="governance-metric__value">{{ formatPercent(quality.zero_result_rate) }}</span>
          </div>
          <div class="governance-metric">
            <span class="governance-metric__label">{{ t('governanceConsole.permissionFiltered') }}</span>
            <span class="governance-metric__value">{{ quality.permission_filtered_total }}</span>
          </div>
          <div class="governance-metric">
            <span class="governance-metric__label">{{ t('governanceConsole.averageDuration') }}</span>
            <span class="governance-metric__value">
              {{ Math.round(quality.average_duration_ms || 0) }}
            </span>
          </div>
        </div>
        <h3 class="governance-console__section">{{ t('governanceConsole.zeroResultQueries') }}</h3>
        <t-table
          row-key="query"
          :data="zeroResultQueries"
          :columns="frequentColumns"
          size="small"
          :loading="loading"
        />
      </t-tab-panel>

      <!-- ================= 变更信号（§7.3） ================= -->
      <t-tab-panel value="signals" :label="t('governanceConsole.tabSignals')">
        <t-table
          row-key="id"
          :data="signals"
          :columns="signalColumns"
          size="medium"
          hover
          :loading="loading"
          :empty="t('governanceConsole.emptySignals')"
        >
          <template #summary="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.summary }}</span>
              <span class="governance-subject__meta">
                {{ signalTypeLabel(row.signal_type) }} · {{ row.source_id }}
              </span>
            </div>
          </template>
          <template #confidence="{ row }">
            <t-tag size="small" variant="light"
              :theme="row.confidence === 'high' ? 'danger' : row.confidence === 'mid' ? 'warning' : 'default'">
              {{ row.confidence }}
            </t-tag>
          </template>
          <template #op="{ row }">
            <div class="governance-actions">
              <t-button theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="runSignal(row, 'convert')">
                {{ t('governanceConsole.signalConvert') }}
              </t-button>
              <t-button theme="danger" variant="text" size="small"
                :loading="busyId === row.id" @click="runSignal(row, 'dismiss')">
                {{ t('governanceConsole.signalDismiss') }}
              </t-button>
            </div>
          </template>
        </t-table>
      </t-tab-panel>

      <!-- ================= 变更单（§7.2 闭环） ================= -->
      <t-tab-panel value="changeRequests" :label="t('governanceConsole.tabChangeRequests')">
        <p class="governance-console__hint">{{ t('governanceConsole.crGateHint') }}</p>
        <t-table
          row-key="id"
          :data="changeRequests"
          :columns="changeRequestColumns"
          size="medium"
          hover
          :loading="loading"
          :empty="t('governanceConsole.emptyChangeRequests')"
        >
          <template #cr="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.title }}</span>
              <span class="governance-subject__meta">{{ row.cr_number }}</span>
            </div>
          </template>
          <template #change_type="{ row }">{{ row.change_type }}</template>
          <template #counts="{ row }">
            <span>{{ (row.items || []).length }} / {{ (row.impact_tasks || []).length }}</span>
          </template>
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="crStatusMeta(row.status).theme">
              {{ crStatusMeta(row.status).label }}
            </t-tag>
          </template>
          <template #op="{ row }">
            <div class="governance-actions">
              <t-button v-if="row.status === 'draft'" variant="text" size="small"
                :loading="busyId === row.id" @click="runCR(row, 'analyze')">
                {{ t('governanceConsole.crAnalyze') }}
              </t-button>
              <t-button v-if="row.status === 'analyzing'" theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="runCR(row, 'submit')">
                {{ t('governanceConsole.crSubmit') }}
              </t-button>
              <t-button v-if="row.status === 'in_approval'" theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="runCR(row, 'schedule')">
                {{ t('governanceConsole.crSchedule') }}
              </t-button>
              <t-button v-if="row.status === 'scheduled'" theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="runCR(row, 'publish')">
                {{ t('governanceConsole.crPublish') }}
              </t-button>
              <t-button v-if="row.status === 'verifying'" variant="text" size="small"
                :loading="busyId === row.id" @click="promptVerify(row)">
                {{ t('governanceConsole.crVerify') }}
              </t-button>
              <t-button v-if="row.status === 'verifying'" variant="text" size="small"
                :loading="busyId === row.id" @click="runCR(row, 'notify')">
                {{ t('governanceConsole.crNotify') }}
              </t-button>
              <t-button v-if="row.status === 'verifying'" theme="success" variant="text" size="small"
                :loading="busyId === row.id" @click="runCR(row, 'close')">
                {{ t('governanceConsole.crClose') }}
              </t-button>
              <t-button v-if="canRollback(row)" theme="danger" variant="text" size="small"
                :loading="busyId === row.id" @click="runCR(row, 'rollback')">
                {{ t('governanceConsole.crRollback') }}
              </t-button>
            </div>
          </template>
        </t-table>
      </t-tab-panel>

      <!-- ================= 知识缺口（§14.4） ================= -->
      <t-tab-panel value="gaps" :label="t('governanceConsole.tabGaps')">
        <t-table
          row-key="id"
          :data="gaps"
          :columns="gapColumns"
          size="medium"
          hover
          :loading="loading"
          :empty="t('governanceConsole.emptyGaps')"
        >
          <template #query="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.query }}</span>
              <span class="governance-subject__meta">{{ row.source }}</span>
            </div>
          </template>
          <template #last_seen_at="{ row }">{{ formatTime(row.last_seen_at) }}</template>
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="gapStatusMeta(row.status).theme">
              {{ gapStatusMeta(row.status).label }}
            </t-tag>
          </template>
          <template #op="{ row }">
            <div class="governance-actions">
              <t-button v-if="row.status === 'open'" theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="assignGap(row)">
                {{ t('governanceConsole.gapAssign') }}
              </t-button>
              <t-button v-if="row.status !== 'closed'" theme="danger" variant="text" size="small"
                :loading="busyId === row.id" @click="closeGap(row)">
                {{ t('governanceConsole.close') }}
              </t-button>
            </div>
          </template>
        </t-table>
      </t-tab-panel>

      <!-- ================= 外部订阅（§7.3 法规/标准采集） ================= -->
      <t-tab-panel value="feeds" :label="t('governanceConsole.tabFeeds')">
        <div class="governance-toolbar">
          <t-button theme="primary" size="small" @click="openFeedDialog()">
            {{ t('governanceConsole.feedAdd') }}
          </t-button>
          <span class="governance-hint">{{ t('governanceConsole.feedHint') }}</span>
        </div>
        <t-table
          row-key="id"
          :data="feeds"
          :columns="feedColumns"
          size="medium"
          hover
          :loading="loading"
          :empty="t('governanceConsole.emptyFeeds')"
        >
          <template #feed="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.name }}</span>
              <span class="governance-subject__meta">{{ row.feed_url }}</span>
              <span class="governance-subject__meta">
                {{ row.domain_tags && row.domain_tags.length
                  ? row.domain_tags.join(' / ')
                  : t('governanceConsole.feedNoDomain') }}
              </span>
            </div>
          </template>
          <template #enabled="{ row }">
            <t-switch
              size="small"
              :value="row.enabled"
              :loading="busyId === row.id"
              @change="(value: any) => toggleFeed(row, Boolean(value))"
            />
          </template>
          <template #last_checked_at="{ row }">
            {{ row.last_checked_at ? formatTime(row.last_checked_at) : t('governanceConsole.feedNeverChecked') }}
          </template>
          <template #feedStatus="{ row }">
            <t-tag v-if="row.last_error" size="small" variant="light" theme="danger">
              {{ t('governanceConsole.feedFailed') }}
            </t-tag>
            <t-tag v-else size="small" variant="light" theme="success">
              {{ t('governanceConsole.feedOk') }}
            </t-tag>
          </template>
          <template #op="{ row }">
            <div class="governance-actions">
              <t-button theme="primary" variant="text" size="small"
                :loading="busyId === row.id" @click="syncFeed(row)">
                {{ t('governanceConsole.feedSync') }}
              </t-button>
              <t-button variant="text" size="small" @click="openEntries(row)">
                {{ t('governanceConsole.feedEntries') }}
              </t-button>
              <t-button variant="text" size="small" @click="openFeedDialog(row)">
                {{ t('governanceConsole.feedEdit') }}
              </t-button>
              <t-popconfirm :content="t('governanceConsole.feedDeleteConfirm')"
                @confirm="removeFeed(row)">
                <t-button theme="danger" variant="text" size="small"
                  :loading="busyId === row.id">
                  {{ t('governanceConsole.feedDelete') }}
                </t-button>
              </t-popconfirm>
            </div>
          </template>
        </t-table>

        <div v-if="lastSync" class="governance-sync-result">
          {{ t('governanceConsole.feedSyncResult', {
            fetched: lastSync.fetched,
            created: lastSync.new_entries,
            matched: lastSync.matched_knowledge,
            signals: lastSync.signals,
            unmatched: lastSync.unmatched_entries,
          }) }}
        </div>
      </t-tab-panel>

      <!-- ================= 嵌入式推送（§15.5 上下文推送） ================= -->
      <t-tab-panel value="push" :label="t('governanceConsole.tabPush')">
        <div class="governance-toolbar">
          <t-button theme="primary" size="small" @click="openPushDialog()">
            {{ t('governanceConsole.pushAdd') }}
          </t-button>
          <span class="governance-hint">{{ t('governanceConsole.pushHint') }}</span>
        </div>
        <t-table
          row-key="id"
          :data="pushRules"
          :columns="pushRuleColumns"
          size="medium"
          hover
          :loading="loading"
          :empty="t('governanceConsole.emptyPushRules')"
        >
          <template #rule="{ row }">
            <div class="governance-subject">
              <span class="governance-subject__title">{{ row.name }}</span>
              <span class="governance-subject__meta">
                {{ pushRuleScope(row) }}
              </span>
              <span class="governance-subject__meta">
                {{ row.tag_names?.length
                  ? row.tag_names.join(' / ')
                  : (row.knowledge_ids || []).join(', ') }}
              </span>
            </div>
          </template>
          <template #enabled="{ row }">
            <t-switch size="small" :value="row.enabled"
              @change="(value: any) => togglePushRule(row, Boolean(value))" />
          </template>
          <template #op="{ row }">
            <div class="governance-actions">
              <t-button variant="text" size="small" @click="openPushDialog(row)">
                {{ t('governanceConsole.feedEdit') }}
              </t-button>
              <t-popconfirm :content="t('governanceConsole.pushDeleteConfirm')"
                @confirm="removePushRule(row)">
                <t-button theme="danger" variant="text" size="small">
                  {{ t('governanceConsole.feedDelete') }}
                </t-button>
              </t-popconfirm>
            </div>
          </template>
        </t-table>

        <h4 class="governance-subtitle">{{ t('governanceConsole.pushStatsTitle') }}</h4>
        <t-table
          row-key="rule_id"
          :data="pushStats"
          :columns="pushStatColumns"
          size="small"
          :empty="t('governanceConsole.emptyPushStats')"
        />
      </t-tab-panel>
    </t-tabs>

    <t-dialog
      v-model:visible="feedDialogVisible"
      :header="editingFeedId ? t('governanceConsole.feedEdit') : t('governanceConsole.feedAdd')"
      width="640px"
      :confirm-btn="t('governanceConsole.feedSave')"
      @confirm="saveFeed"
    >
      <t-form label-width="150px" label-align="right">
        <t-form-item :label="t('governanceConsole.feedName')">
          <t-input v-model="feedForm.name" :placeholder="t('governanceConsole.feedNamePlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.feedUrl')">
          <t-input v-model="feedForm.feed_url" placeholder="https://www.sac.gov.cn/rss.xml" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.feedFormat')">
          <t-select v-model="feedForm.format">
            <t-option value="auto" :label="t('governanceConsole.feedFormatAuto')" />
            <t-option value="rss" label="RSS 2.0" />
            <t-option value="atom" label="Atom" />
            <t-option value="json" label="JSON Feed" />
          </t-select>
        </t-form-item>
        <t-form-item :label="t('governanceConsole.feedDomainTags')">
          <t-input v-model="feedForm.domain_tags"
            :placeholder="t('governanceConsole.feedDomainPlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.feedKbIds')">
          <t-input v-model="feedForm.knowledge_base_ids"
            :placeholder="t('governanceConsole.feedKbPlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.feedInterval')">
          <t-input-number v-model="feedForm.check_interval_hours" :min="1" :max="720" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.feedAuthHeader')">
          <t-input v-model="feedForm.auth_header"
            :placeholder="t('governanceConsole.feedAuthPlaceholder')" />
        </t-form-item>
        <t-form-item>
          <span class="governance-hint">{{ t('governanceConsole.feedAuthHint') }}</span>
        </t-form-item>
      </t-form>
    </t-dialog>

    <t-dialog
      v-model:visible="groupDialogVisible"
      :header="editingGroupId ? t('governanceConsole.feedEdit') : t('governanceConsole.groupAdd')"
      width="520px"
      :confirm-btn="t('governanceConsole.feedSave')"
      @confirm="saveGroup"
    >
      <t-form label-width="110px" label-align="right">
        <t-form-item :label="t('governanceConsole.groupName')">
          <t-input v-model="groupForm.name" :placeholder="t('governanceConsole.groupNamePlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.groupDesc')">
          <t-input v-model="groupForm.description" />
        </t-form-item>
      </t-form>
    </t-dialog>

    <t-dialog
      v-model:visible="membersVisible"
      :header="t('governanceConsole.groupMembersTitle', { name: membersGroupName })"
      width="560px"
      :footer="false"
    >
      <div class="governance-toolbar">
        <t-input v-model="newMemberId" :placeholder="t('governanceConsole.groupMemberPlaceholder')" />
        <t-button theme="primary" size="small" :loading="membersBusy" @click="addMember">
          {{ t('governanceConsole.feedAdd') }}
        </t-button>
      </div>
      <t-table
        row-key="user_id"
        :data="memberRows"
        :columns="memberColumns"
        size="small"
        :empty="t('governanceConsole.emptyMembers')"
      >
        <template #op="{ row }">
          <t-button theme="danger" variant="text" size="small"
            :loading="busyId === row.user_id" @click="removeMember(row)">
            {{ t('governanceConsole.feedDelete') }}
          </t-button>
        </template>
      </t-table>
      <p class="governance-hint">{{ t('governanceConsole.groupMembersHint') }}</p>
    </t-dialog>

    <t-dialog
      v-model:visible="pushDialogVisible"
      :header="editingPushId ? t('governanceConsole.pushEdit') : t('governanceConsole.pushAdd')"
      width="640px"
      :confirm-btn="t('governanceConsole.feedSave')"
      @confirm="savePushRule"
    >
      <t-form label-width="150px" label-align="right">
        <t-form-item :label="t('governanceConsole.feedName')">
          <t-input v-model="pushForm.name" :placeholder="t('governanceConsole.pushNamePlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushSystem')">
          <t-input v-model="pushForm.system" placeholder="mes" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushScene')">
          <t-input v-model="pushForm.scene" placeholder="work_order" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushEntityType')">
          <t-input v-model="pushForm.entity_type" placeholder="equipment" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushEntityValue')">
          <t-input v-model="pushForm.entity_value" placeholder="E45" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushTags')">
          <t-input v-model="pushForm.tag_names"
            :placeholder="t('governanceConsole.pushTagsPlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushKnowledgeIds')">
          <t-input v-model="pushForm.knowledge_ids"
            :placeholder="t('governanceConsole.pushKnowledgePlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushReason')">
          <t-input v-model="pushForm.reason" :placeholder="t('governanceConsole.pushReasonPlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('governanceConsole.pushPriority')">
          <t-input-number v-model="pushForm.priority" :min="0" :max="1000" />
        </t-form-item>
        <t-form-item>
          <span class="governance-hint">{{ t('governanceConsole.pushMatchHint') }}</span>
        </t-form-item>
      </t-form>
    </t-dialog>

    <t-dialog
      v-model:visible="entriesVisible"
      :header="t('governanceConsole.feedEntriesTitle', { name: entriesFeedName })"
      width="720px"
      :footer="false"
    >
      <t-table
        row-key="id"
        :data="entries"
        :columns="entryColumns"
        size="small"
        :empty="t('governanceConsole.emptyEntries')"
      >
        <template #title="{ row }">
          <div class="governance-subject">
            <span class="governance-subject__title">{{ row.title }}</span>
            <span class="governance-subject__meta">{{ row.url }}</span>
          </div>
        </template>
        <template #published_at="{ row }">
          {{ row.published_at ? formatTime(row.published_at) : '—' }}
        </template>
        <template #counts="{ row }">
          {{ t('governanceConsole.feedEntryCounts', {
            matched: row.matched_knowledge_count,
            signals: row.signal_count,
          }) }}
        </template>
      </t-table>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  GOVERNANCE_SUBJECT_TYPES,
  approveOfficialAnswer,
  decideGovernanceApproval,
  getFrequentQueries,
  getSearchQuality,
  listACLCeilingViolations,
  listFieldMigrations,
  listFolderGrants,
  listGovernanceApprovals,
  listKnowledgeGaps,
  listKnowledgeTemplates,
  listOfficialAnswers,
  publishOfficialAnswer,
  retireOfficialAnswer,
  revokeFolderGrant,
  resolveFieldMigration,
  submitOfficialAnswer,
  updateKnowledgeGap,
  analyzeChangeRequest,
  closeChangeRequest,
  listChangeRequests,
  notifyChangeRequest,
  publishChangeRequest,
  rollbackChangeRequest,
  scheduleChangeRequest,
  submitChangeRequest,
  verifyChangeRequest,
  type ChangeRequest,
  convertChangeSignal,
  dismissChangeSignal,
  listChangeSignals,
  createExternalFeed,
  deleteExternalFeed,
  listExternalEntries,
  listExternalFeeds,
  addACLGroupMembers,
  createACLGroup,
  createEmbedPushRule,
  deleteACLGroup,
  deleteEmbedPushRule,
  getEmbedPushRuleStats,
  listACLGroups,
  listACLGroupMembers,
  listEmbedPushRules,
  removeACLGroupMember,
  updateACLGroup,
  updateEmbedPushRule,
  syncExternalFeed,
  updateExternalFeed,
  type ChangeSignal,
  type ExternalSubscriptionEntry,
  type ExternalSubscriptionFeed,
  type ExternalSubscriptionSyncResult,
  type ACLSubjectGroup,
  type EmbedPushRule,
  type EmbedPushRuleStats,
  type GovernanceApprovalTask,
  type KnowledgeGap,
  type KnowledgeTemplate,
  type OfficialAnswer,
  type SearchQualityOverview,
  type FolderGrant,
  type FieldMigration,
  type ACLCeilingViolation,
} from '@/api/governance-console';

/**
 * 治理控制台（KnowAxis V3.0）。
 *
 * 后端在过去两轮里补齐了十余个治理垂直领域，但全部只能通过 API 使用。
 * 这个页面把它们收进一个界面：审批（§6.2 十四类）、官方答案（§13.7）、
 * 模板与字段迁移（§3.2/§4.7）、目录权限与上限违规（§2.3）、搜索质量与
 * 知识缺口（§12.5/§14.4）。
 *
 * 权限：整个页面后端都是 Admin+，403 时以空态呈现而不是错误弹窗——非
 * 管理员本来就不该看到治理数据。
 */

const { t } = useI18n();

type TabValue =
  | 'approvals'
  | 'answers'
  | 'templates'
  | 'permissions'
  | 'searchQuality'
  | 'gaps'
  | 'changeRequests'
  | 'signals'
  | 'feeds'
  | 'push';

const activeTab = ref<TabValue>('approvals');
const loading = ref(false);
const forbidden = ref(false);
const busyId = ref<string | null>(null);

// ---- 审批 ----
const approvals = ref<GovernanceApprovalTask[]>([]);
const subjectFilter = ref<string>('');
const subjectOptions = computed(() =>
  GOVERNANCE_SUBJECT_TYPES.map((value) => ({
    label: t(`governanceConsole.subject_${value}`),
    value,
  })),
);

// ---- 官方答案 ----
const answers = ref<OfficialAnswer[]>([]);

// ---- 模板 ----
const templates = ref<KnowledgeTemplate[]>([]);
const migrations = ref<FieldMigration[]>([]);

// ---- 目录权限 ----
const permissionKbId = ref('');
const folderGrants = ref<FolderGrant[]>([]);
const violations = ref<ACLCeilingViolation[]>([]);

// ---- 搜索质量 ----
const windowDays = ref(30);
const quality = ref<SearchQualityOverview>({
  total_queries: 0,
  zero_result_queries: 0,
  zero_result_rate: 0,
  permission_filtered_total: 0,
  clicked_queries: 0,
  average_duration_ms: 0,
  window_days: 30,
});
const zeroResultQueries = ref<Array<{ query: string; occurrences: number }>>([]);

// ---- 缺口 ----
const gaps = ref<KnowledgeGap[]>([]);

// ---- 变更单 ----
const changeRequests = ref<ChangeRequest[]>([]);

// ---- 变更信号 ----
const signals = ref<ChangeSignal[]>([]);

// ---- 外部订阅（§7.3） ----
const feeds = ref<ExternalSubscriptionFeed[]>([]);
const feedDialogVisible = ref(false);
const editingFeedId = ref('');
const feedForm = ref({
  name: '',
  feed_url: '',
  format: 'auto',
  domain_tags: '',
  knowledge_base_ids: '',
  check_interval_hours: 24,
  auth_header: '',
});
const lastSync = ref<ExternalSubscriptionSyncResult | null>(null);
const entriesVisible = ref(false);
const entriesFeedName = ref('');
const entries = ref<ExternalSubscriptionEntry[]>([]);

// ---- 授权主体组（§2.1） ----
const aclGroups = ref<ACLSubjectGroup[]>([]);
const groupDialogVisible = ref(false);
const editingGroupId = ref('');
const groupForm = ref({ name: '', description: '' });
const membersVisible = ref(false);
const membersGroupId = ref('');
const membersGroupName = ref('');
const memberRows = ref<Array<{ user_id: string }>>([]);
const newMemberId = ref('');
const membersBusy = ref(false);

// ---- 嵌入式推送（§15.5） ----
const pushRules = ref<EmbedPushRule[]>([]);
const pushStats = ref<EmbedPushRuleStats[]>([]);
const pushDialogVisible = ref(false);
const editingPushId = ref('');
const pushForm = ref({
  name: '',
  system: '',
  scene: '',
  entity_type: '',
  entity_value: '',
  tag_names: '',
  knowledge_ids: '',
  reason: '',
  priority: 0,
});

// ---------------------------------------------------------------------------
// 展示助手
// ---------------------------------------------------------------------------

const formatTime = (value?: string | null) => {
  if (!value) return '--';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '--';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

const formatDate = (value?: string | null) => {
  if (!value) return '--';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '--';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
};

const formatPercent = (value: number) => `${((value || 0) * 100).toFixed(1)}%`;

type TagTheme = 'default' | 'success' | 'warning' | 'danger';

const riskMeta = (level: string): { label: string; theme: TagTheme } => {
  switch (level) {
    case 'high':
      return { label: t('governanceConsole.riskHigh'), theme: 'danger' };
    case 'mid':
      return { label: t('governanceConsole.riskMid'), theme: 'warning' };
    case 'low':
      return { label: t('governanceConsole.riskLow'), theme: 'success' };
    default:
      return { label: level || '--', theme: 'default' };
  }
};

const statusMeta = (status: string): { label: string; theme: TagTheme } => {
  switch (status) {
    case 'pending':
      return { label: t('governanceConsole.statusPending'), theme: 'warning' };
    case 'approved':
      return { label: t('governanceConsole.statusApproved'), theme: 'success' };
    case 'rejected':
      return { label: t('governanceConsole.statusRejected'), theme: 'danger' };
    case 'canceled':
      return { label: t('governanceConsole.statusCanceled'), theme: 'default' };
    default:
      return { label: status || '--', theme: 'default' };
  }
};

const answerStatusMeta = (status: string): { label: string; theme: TagTheme } => {
  switch (status) {
    case 'draft':
      return { label: t('governanceConsole.statusDraft'), theme: 'default' };
    case 'in_review':
      return { label: t('governanceConsole.statusInReview'), theme: 'warning' };
    case 'approved':
      return { label: t('governanceConsole.statusApproved'), theme: 'success' };
    case 'published':
      return { label: t('governanceConsole.statusPublished'), theme: 'success' };
    case 'expired':
      return { label: t('governanceConsole.statusExpired'), theme: 'warning' };
    case 'superseded':
      return { label: t('governanceConsole.statusSuperseded'), theme: 'default' };
    case 'retired':
      return { label: t('governanceConsole.statusRetired'), theme: 'default' };
    default:
      return { label: status || '--', theme: 'default' };
  }
};

const gapStatusMeta = (status: string): { label: string; theme: TagTheme } => {
  switch (status) {
    case 'open':
      return { label: t('governanceConsole.statusOpen'), theme: 'warning' };
    case 'acknowledged':
      return { label: t('governanceConsole.statusAcknowledged'), theme: 'default' };
    case 'closed':
      return { label: t('governanceConsole.statusClosed'), theme: 'success' };
    default:
      return { label: status || '--', theme: 'default' };
  }
};

const crStatusMeta = (status: string): { label: string; theme: TagTheme } => {
  switch (status) {
    case 'draft':
      return { label: t('governanceConsole.statusDraft'), theme: 'default' };
    case 'analyzing':
      return { label: t('governanceConsole.statusAnalyzing'), theme: 'default' };
    case 'in_approval':
      return { label: t('governanceConsole.statusInApproval'), theme: 'warning' };
    case 'scheduled':
      return { label: t('governanceConsole.statusScheduled'), theme: 'warning' };
    case 'publishing':
      return { label: t('governanceConsole.statusPublishing'), theme: 'warning' };
    case 'verifying':
      return { label: t('governanceConsole.statusVerifying'), theme: 'success' };
    case 'closed':
      return { label: t('governanceConsole.statusClosed'), theme: 'success' };
    case 'rolled_back':
      return { label: t('governanceConsole.statusRolledBack'), theme: 'danger' };
    default:
      return { label: status || '--', theme: 'default' };
  }
};

// 回滚只在验证阶段可用：更早的阶段没有可回滚的已应用条目，关闭后则无从回滚。
const canRollback = (row: ChangeRequest) => row.status === 'verifying';

const subjectLabel = (value: string) => t(`governanceConsole.subject_${value}`);

const isOverdue = (row: GovernanceApprovalTask) =>
  row.status === 'pending' && !!row.sla_due && new Date(row.sla_due).getTime() <= Date.now();

const isAnswerLive = (row: OfficialAnswer) =>
  row.status === 'published' || row.status === 'approved' || row.status === 'expired';

// ---------------------------------------------------------------------------
// 表格列
// ---------------------------------------------------------------------------

const approvalColumns = computed(() => [
  { colKey: 'subject', title: t('governanceConsole.columnSubject'), ellipsis: true, minWidth: 260 },
  { colKey: 'risk_level', title: t('governanceConsole.columnRisk'), width: 90 },
  { colKey: 'requested_by', title: t('governanceConsole.columnRequester'), width: 150, ellipsis: true },
  { colKey: 'sla_due', title: t('governanceConsole.columnSla'), width: 150 },
  { colKey: 'status', title: t('governanceConsole.columnStatus'), width: 110 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 150 },
]);

const answerColumns = computed(() => [
  { colKey: 'question', title: t('governanceConsole.columnQuery'), ellipsis: true, minWidth: 260 },
  { colKey: 'status', title: t('governanceConsole.columnStatus'), width: 110 },
  { colKey: 'owner_id', title: t('governanceConsole.answerOwner'), width: 130, ellipsis: true },
  { colKey: 'expires_at', title: t('governanceConsole.answerExpires'), width: 130 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 170 },
]);

const templateColumns = computed(() => [
  { colKey: 'name', title: t('governanceConsole.templateName'), ellipsis: true, minWidth: 180 },
  { colKey: 'template_key', title: t('governanceConsole.templateKey'), width: 180, ellipsis: true },
  { colKey: 'version', title: t('governanceConsole.columnVersion'), width: 90 },
  { colKey: 'fieldCount', title: t('governanceConsole.columnFields'), width: 100 },
]);

const migrationColumns = computed(() => [
  { colKey: 'field_key', title: t('governanceConsole.fieldKey'), ellipsis: true, minWidth: 140 },
  { colKey: 'change_kind', title: t('governanceConsole.columnType'), width: 110 },
  { colKey: 'affected_count', title: t('governanceConsole.columnOccurrences'), width: 90 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 160 },
]);

const grantColumns = computed(() => [
  { colKey: 'folder_path', title: t('governanceConsole.folderPath'), ellipsis: true, minWidth: 160 },
  { colKey: 'subject_id', title: t('governanceConsole.subjectId'), ellipsis: true, minWidth: 130 },
  { colKey: 'action', title: t('governanceConsole.action'), width: 110 },
  { colKey: 'effect', title: t('governanceConsole.effect'), width: 90 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 90 },
]);

const violationColumns = computed(() => [
  { colKey: 'resource_id', title: t('governanceConsole.folderPath'), ellipsis: true, minWidth: 150 },
  { colKey: 'kind', title: t('governanceConsole.columnType'), width: 150 },
  { colKey: 'detected_at', title: t('governanceConsole.columnLastSeen'), width: 150 },
]);

const frequentColumns = computed(() => [
  { colKey: 'query', title: t('governanceConsole.columnQuery'), ellipsis: true, minWidth: 240 },
  { colKey: 'occurrences', title: t('governanceConsole.columnOccurrences'), width: 110 },
]);

// signalTypeLabel resolves the §7.3 signal class label.
const signalTypeLabel = (value: string) => {
  const key = `governanceConsole.signalType_${value}`;
  const label = t(key);
  return label === key ? value : label;
};

const signalColumns = computed(() => [
  { colKey: 'summary', title: t('governanceConsole.columnSubject'), ellipsis: true, minWidth: 300 },
  { colKey: 'confidence', title: t('governanceConsole.columnConfidence'), width: 110 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 180 },
]);

const feedColumns = computed(() => [
  { colKey: 'feed', title: t('governanceConsole.columnFeed'), ellipsis: true, minWidth: 280 },
  { colKey: 'enabled', title: t('governanceConsole.columnEnabled'), width: 90 },
  { colKey: 'last_checked_at', title: t('governanceConsole.columnLastChecked'), width: 170 },
  { colKey: 'feedStatus', title: t('governanceConsole.columnStatus'), width: 100 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 300 },
]);

const groupColumns = computed(() => [
  { colKey: 'group', title: t('governanceConsole.groupName'), ellipsis: true, minWidth: 220 },
  { colKey: 'member_count', title: t('governanceConsole.groupMemberCount'), width: 110 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 240 },
]);

const memberColumns = computed(() => [
  { colKey: 'user_id', title: t('governanceConsole.groupMemberId'), ellipsis: true, minWidth: 220 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 100 },
]);

const pushRuleColumns = computed(() => [
  { colKey: 'rule', title: t('governanceConsole.columnPushRule'), ellipsis: true, minWidth: 300 },
  { colKey: 'priority', title: t('governanceConsole.pushPriority'), width: 100 },
  { colKey: 'enabled', title: t('governanceConsole.columnEnabled'), width: 90 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 160 },
]);

const pushStatColumns = computed(() => [
  { colKey: 'rule_name', title: t('governanceConsole.columnPushRule'), ellipsis: true, minWidth: 240 },
  { colKey: 'impressions', title: t('governanceConsole.pushImpressions'), width: 120 },
  { colKey: 'clicks', title: t('governanceConsole.pushClicks'), width: 110 },
  { colKey: 'dismissals', title: t('governanceConsole.pushDismissals'), width: 110 },
]);

const entryColumns = computed(() => [
  { colKey: 'title', title: t('governanceConsole.columnFeed'), ellipsis: true, minWidth: 320 },
  { colKey: 'published_at', title: t('governanceConsole.columnPublished'), width: 140 },
  { colKey: 'counts', title: t('governanceConsole.columnItems'), width: 180 },
]);

const changeRequestColumns = computed(() => [
  { colKey: 'cr', title: t('governanceConsole.columnCrTitle'), ellipsis: true, minWidth: 240 },
  { colKey: 'change_type', title: t('governanceConsole.columnChangeType'), width: 140 },
  { colKey: 'counts', title: t('governanceConsole.columnItems'), width: 130 },
  { colKey: 'status', title: t('governanceConsole.columnStatus'), width: 110 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 260 },
]);

const gapColumns = computed(() => [
  { colKey: 'query', title: t('governanceConsole.gapQuery'), ellipsis: true, minWidth: 240 },
  { colKey: 'occurrences', title: t('governanceConsole.gapOccurrences'), width: 110 },
  { colKey: 'last_seen_at', title: t('governanceConsole.columnLastSeen'), width: 150 },
  { colKey: 'status', title: t('governanceConsole.columnStatus'), width: 110 },
  { colKey: 'op', title: t('governanceConsole.columnOp'), width: 180 },
]);

// ---------------------------------------------------------------------------
// 加载
// ---------------------------------------------------------------------------

const handleError = (error: any) => {
  const status = error?.response?.status ?? error?.status;
  if (status === 403 || status === 401) {
    forbidden.value = true;
    return;
  }
  MessagePlugin.error(error?.message || t('governanceConsole.loadFailed'));
};

const withLoading = async (work: () => Promise<void>) => {
  loading.value = true;
  try {
    await work();
  } catch (error) {
    handleError(error);
  } finally {
    loading.value = false;
  }
};

const loadApprovals = () =>
  withLoading(async () => {
    const res = await listGovernanceApprovals(subjectFilter.value as any);
    approvals.value = res?.data || [];
  });

const loadAnswers = () =>
  withLoading(async () => {
    const res = await listOfficialAnswers();
    answers.value = res?.data || [];
  });

const loadTemplates = () =>
  withLoading(async () => {
    const [templateRes, migrationRes] = await Promise.all([
      listKnowledgeTemplates(),
      listFieldMigrations('pending'),
    ]);
    templates.value = templateRes?.data || [];
    migrations.value = migrationRes?.data || [];
  });

const loadPermissions = () =>
  withLoading(async () => {
    const groupRes = await listACLGroups().catch(() => ({ data: [] as ACLSubjectGroup[] }));
    aclGroups.value = groupRes?.data || [];
    const violationRes = await listACLCeilingViolations();
    violations.value = violationRes?.data || [];
    if (permissionKbId.value) {
      const grantRes = await listFolderGrants(permissionKbId.value);
      folderGrants.value = grantRes?.data || [];
    } else {
      folderGrants.value = [];
    }
  });

const loadSearchQuality = () =>
  withLoading(async () => {
    const [overviewRes, zeroRes] = await Promise.all([
      getSearchQuality(windowDays.value),
      getFrequentQueries({ windowDays: windowDays.value, zeroOnly: true, limit: 20 }),
    ]);
    quality.value = overviewRes?.data || quality.value;
    zeroResultQueries.value = zeroRes?.data || [];
  });

const loadSignals = () =>
  withLoading(async () => {
    const res = await listChangeSignals();
    signals.value = res?.data || [];
  });

// runSignal converts a signal into a CR draft or dismisses it; converting
// creates a change request, so the change-request tab is refreshed too.
const runSignal = async (row: ChangeSignal, action: 'convert' | 'dismiss') => {
  busyId.value = row.id;
  try {
    if (action === 'convert') {
      await convertChangeSignal(row.id);
    } else {
      await dismissChangeSignal(row.id);
    }
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadSignals();
    if (action === 'convert') {
      await loadChangeRequests();
    }
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const loadFeeds = () =>
  withLoading(async () => {
    const res = await listExternalFeeds();
    feeds.value = res?.data || [];
  });

// openFeedDialog opens the create form, or the edit form pre-filled from a row.
const openFeedDialog = (row?: ExternalSubscriptionFeed) => {
  editingFeedId.value = row?.id || '';
  feedForm.value = {
    name: row?.name || '',
    feed_url: row?.feed_url || '',
    format: row?.format || 'auto',
    domain_tags: (row?.domain_tags || []).join(', '),
    knowledge_base_ids: (row?.knowledge_base_ids || []).join(', '),
    check_interval_hours: row?.check_interval_hours || 24,
    auth_header: '',
  };
  feedDialogVisible.value = true;
};

const splitList = (value: string): string[] =>
  value
    .split(/[,，]/)
    .map((item) => item.trim())
    .filter(Boolean);

const saveFeed = async () => {
  const payload = {
    name: feedForm.value.name.trim(),
    feed_url: feedForm.value.feed_url.trim(),
    format: feedForm.value.format,
    domain_tags: splitList(feedForm.value.domain_tags),
    knowledge_base_ids: splitList(feedForm.value.knowledge_base_ids),
    check_interval_hours: feedForm.value.check_interval_hours,
  };
  try {
    if (editingFeedId.value) {
      await updateExternalFeed(editingFeedId.value, {
        ...payload,
        // 只在用户输入时提交认证头；留空表示保持原值。
        ...(feedForm.value.auth_header.trim()
          ? { auth_header: feedForm.value.auth_header.trim() }
          : {}),
      });
    } else {
      await createExternalFeed({
        ...payload,
        auth_header: feedForm.value.auth_header.trim(),
      });
    }
    feedDialogVisible.value = false;
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadFeeds();
  } catch (error) {
    handleError(error);
  }
};

const toggleFeed = async (row: ExternalSubscriptionFeed, enabled: boolean) => {
  busyId.value = row.id;
  try {
    await updateExternalFeed(row.id, { enabled });
    await loadFeeds();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const syncFeed = async (row: ExternalSubscriptionFeed) => {
  busyId.value = row.id;
  try {
    const res = await syncExternalFeed(row.id);
    lastSync.value = res?.data || null;
    await loadFeeds();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const removeFeed = async (row: ExternalSubscriptionFeed) => {
  busyId.value = row.id;
  try {
    await deleteExternalFeed(row.id);
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadFeeds();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const openEntries = async (row: ExternalSubscriptionFeed) => {
  entriesFeedName.value = row.name;
  entries.value = [];
  entriesVisible.value = true;
  try {
    const res = await listExternalEntries(row.id);
    entries.value = res?.data || [];
  } catch (error) {
    handleError(error);
  }
};

const loadACLGroups = () =>
  withLoading(async () => {
    const res = await listACLGroups();
    aclGroups.value = res?.data || [];
  });

const openGroupDialog = (row?: ACLSubjectGroup) => {
  editingGroupId.value = row?.id || '';
  groupForm.value = { name: row?.name || '', description: row?.description || '' };
  groupDialogVisible.value = true;
};

const saveGroup = async () => {
  const payload = {
    name: groupForm.value.name.trim(),
    description: groupForm.value.description.trim(),
  };
  try {
    if (editingGroupId.value) {
      await updateACLGroup(editingGroupId.value, payload);
    } else {
      await createACLGroup(payload);
    }
    groupDialogVisible.value = false;
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadACLGroups();
  } catch (error) {
    handleError(error);
  }
};

const removeGroup = async (row: ACLSubjectGroup) => {
  busyId.value = row.id;
  try {
    await deleteACLGroup(row.id);
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadACLGroups();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const openMembers = async (row: ACLSubjectGroup) => {
  membersGroupId.value = row.id;
  membersGroupName.value = row.name;
  memberRows.value = [];
  newMemberId.value = '';
  membersVisible.value = true;
  try {
    const res = await listACLGroupMembers(row.id);
    memberRows.value = (res?.data || []).map((userID) => ({ user_id: userID }));
  } catch (error) {
    handleError(error);
  }
};

const addMember = async () => {
  const userID = newMemberId.value.trim();
  if (!userID) return;
  membersBusy.value = true;
  try {
    await addACLGroupMembers(membersGroupId.value, [userID]);
    newMemberId.value = '';
    const res = await listACLGroupMembers(membersGroupId.value);
    memberRows.value = (res?.data || []).map((id) => ({ user_id: id }));
    await loadACLGroups();
  } catch (error) {
    handleError(error);
  } finally {
    membersBusy.value = false;
  }
};

const removeMember = async (row: { user_id: string }) => {
  busyId.value = row.user_id;
  try {
    await removeACLGroupMember(membersGroupId.value, row.user_id);
    memberRows.value = memberRows.value.filter((item) => item.user_id !== row.user_id);
    await loadACLGroups();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const loadPush = () =>
  withLoading(async () => {
    const [ruleRes, statRes] = await Promise.all([
      listEmbedPushRules(),
      getEmbedPushRuleStats(30),
    ]);
    pushRules.value = ruleRes?.data || [];
    pushStats.value = statRes?.data || [];
  });

const openPushDialog = (row?: EmbedPushRule) => {
  editingPushId.value = row?.id || '';
  pushForm.value = {
    name: row?.name || '',
    system: row?.system || '',
    scene: row?.scene || '',
    entity_type: row?.entity_type || '',
    entity_value: row?.entity_value || '',
    tag_names: (row?.tag_names || []).join(', '),
    knowledge_ids: (row?.knowledge_ids || []).join(', '),
    reason: row?.reason || '',
    priority: row?.priority || 0,
  };
  pushDialogVisible.value = true;
};

const savePushRule = async () => {
  const payload = {
    name: pushForm.value.name.trim(),
    system: pushForm.value.system.trim(),
    scene: pushForm.value.scene.trim(),
    entity_type: pushForm.value.entity_type.trim(),
    entity_value: pushForm.value.entity_value.trim(),
    tag_names: splitList(pushForm.value.tag_names),
    knowledge_ids: splitList(pushForm.value.knowledge_ids),
    reason: pushForm.value.reason.trim(),
    priority: pushForm.value.priority,
  };
  try {
    if (editingPushId.value) {
      await updateEmbedPushRule(editingPushId.value, payload);
    } else {
      await createEmbedPushRule(payload);
    }
    pushDialogVisible.value = false;
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadPush();
  } catch (error) {
    handleError(error);
  }
};

const togglePushRule = async (row: EmbedPushRule, enabled: boolean) => {
  busyId.value = row.id;
  try {
    await updateEmbedPushRule(row.id, {
      name: row.name,
      system: row.system,
      scene: row.scene,
      entity_type: row.entity_type,
      entity_value: row.entity_value,
      tag_names: row.tag_names || [],
      knowledge_ids: row.knowledge_ids || [],
      reason: row.reason,
      priority: row.priority,
      enabled,
    });
    await loadPush();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const removePushRule = async (row: EmbedPushRule) => {
  busyId.value = row.id;
  try {
    await deleteEmbedPushRule(row.id);
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadPush();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const pushRuleScope = (row: EmbedPushRule) => {
  const parts = [row.system, row.scene, row.entity_type]
    .map((value) => (value || '').trim())
    .filter(Boolean);
  const entity = (row.entity_value || '').trim();
  return parts.length || entity
    ? `${parts.join(' / ') || t('governanceConsole.pushAny')}${entity ? ` · ${entity}` : ''}`
    : t('governanceConsole.pushAny');
};

const loadChangeRequests = () =>
  withLoading(async () => {
    const res = await listChangeRequests();
    changeRequests.value = res?.data || [];
  });

const loadGaps = () =>
  withLoading(async () => {
    const res = await listKnowledgeGaps();
    gaps.value = res?.data || [];
  });

const reloadActiveTab = () => {
  forbidden.value = false;
  switch (activeTab.value) {
    case 'approvals':
      return loadApprovals();
    case 'answers':
      return loadAnswers();
    case 'templates':
      return loadTemplates();
    case 'permissions':
      return loadPermissions();
    case 'searchQuality':
      return loadSearchQuality();
    case 'gaps':
      return loadGaps();
    case 'changeRequests':
      return loadChangeRequests();
    case 'signals':
      return loadSignals();
    case 'feeds':
      return loadFeeds();
    case 'push':
      return loadPush();
    default:
      return Promise.resolve();
  }
};

const onTabChange = (value: string | number) => {
  activeTab.value = value as TabValue;
  void reloadActiveTab();
};

// ---------------------------------------------------------------------------
// 动作
// ---------------------------------------------------------------------------

const decide = async (row: GovernanceApprovalTask, decision: 'approved' | 'rejected') => {
  busyId.value = row.id;
  try {
    await decideGovernanceApproval(row.id, { decision });
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadApprovals();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const submitAnswer = async (row: OfficialAnswer) => {
  busyId.value = row.id;
  try {
    await submitOfficialAnswer(row.id);
    await loadAnswers();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const approveAnswer = async (row: OfficialAnswer) => {
  busyId.value = row.id;
  try {
    // 高风险答案需要回执：这里留空，由后端返回明确错误提示需要先走审批。
    await approveOfficialAnswer(row.id);
    await loadAnswers();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const publishAnswer = async (row: OfficialAnswer) => {
  busyId.value = row.id;
  try {
    await publishOfficialAnswer(row.id);
    await loadAnswers();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const retireAnswer = async (row: OfficialAnswer) => {
  busyId.value = row.id;
  try {
    await retireOfficialAnswer(row.id);
    await loadAnswers();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const resolveMigration = async (row: FieldMigration, status: 'done' | 'waived') => {
  busyId.value = row.id;
  try {
    await resolveFieldMigration(row.id, status);
    await loadTemplates();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const removeGrant = async (row: FolderGrant) => {
  busyId.value = row.id;
  try {
    await revokeFolderGrant(row.knowledge_base_id || permissionKbId.value, row.id);
    await loadPermissions();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

// runCR drives one change-request state transition. The four close gates live
// server-side, so a blocked close surfaces the backend's explanation instead of
// being silently swallowed.
const runCR = async (
  row: ChangeRequest,
  action: 'analyze' | 'submit' | 'schedule' | 'publish' | 'notify' | 'close' | 'rollback',
) => {
  busyId.value = row.id;
  try {
    switch (action) {
      case 'analyze':
        await analyzeChangeRequest(row.id);
        break;
      case 'submit':
        await submitChangeRequest(row.id);
        break;
      case 'schedule':
        await scheduleChangeRequest(row.id);
        break;
      case 'publish':
        await publishChangeRequest(row.id);
        break;
      case 'notify':
        await notifyChangeRequest(row.id);
        break;
      case 'close':
        await closeChangeRequest(row.id);
        break;
      case 'rollback':
        await rollbackChangeRequest(row.id);
        break;
    }
    MessagePlugin.success(t('governanceConsole.confirm'));
    await loadChangeRequests();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

// promptVerify asks for the close-gate verification result (§7.2 condition ②).
const promptVerify = async (row: ChangeRequest) => {
  const result = window.prompt(t('governanceConsole.crVerifyPrompt'), row.verification_result || '');
  if (!result || !result.trim()) {
    return;
  }
  busyId.value = row.id;
  try {
    await verifyChangeRequest(row.id, result.trim());
    await loadChangeRequests();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const assignGap = async (row: KnowledgeGap) => {
  busyId.value = row.id;
  try {
    await updateKnowledgeGap(row.id, { status: 'acknowledged' });
    await loadGaps();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

const closeGap = async (row: KnowledgeGap) => {
  busyId.value = row.id;
  try {
    await updateKnowledgeGap(row.id, { status: 'closed' });
    await loadGaps();
  } catch (error) {
    handleError(error);
  } finally {
    busyId.value = null;
  }
};

onMounted(() => {
  void loadApprovals();
});
</script>

<style scoped>
.governance-console {
  padding: 24px 32px 48px;
  max-width: 1400px;
  margin: 0 auto;
}

.governance-console__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.governance-console__title {
  font-size: var(--app-text-3xl);
  font-weight: 600;
  margin: 0 0 4px;
}

.governance-console__desc {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-md);
}

.governance-console__state {
  padding: 64px 0;
}

.governance-console__toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.governance-console__filter {
  max-width: 260px;
}

.governance-console__hint {
  margin: 0 0 12px;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-md);
}

.governance-console__section {
  font-size: var(--app-text-base);
  font-weight: 600;
  margin: 20px 0 8px;
}

.governance-console__split {
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr);
  gap: 24px;
  align-items: start;
}

.governance-console__pane {
  min-width: 0;
}

.governance-subject {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.governance-subject__title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.governance-subject__meta {
  color: var(--td-text-color-placeholder);
  font-size: var(--app-text-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.governance-actions {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.governance-actions__none {
  color: var(--td-text-color-placeholder);
}

.is-overdue {
  color: var(--td-error-color);
  font-weight: 500;
}

.governance-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.governance-hint {
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
}

.governance-sync-result {
  margin-top: 12px;
  padding: 10px 12px;
  border-radius: var(--app-radius-sm);
  background: var(--td-bg-color-secondarycontainer);
  font-size: var(--app-text-md);
  color: var(--td-text-color-secondary);
}

.governance-console__section--spaced {
  margin-top: 24px;
}

.governance-subtitle {
  font-size: var(--app-text-md);
  font-weight: 500;
  margin: 20px 0 8px;
  color: var(--td-text-color-secondary);
}

.governance-metrics {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
  margin-bottom: 8px;
}

.governance-metric {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 16px;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-md);
  background: var(--td-bg-color-container);
}

.governance-metric__label {
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
}

.governance-metric__value {
  font-size: var(--app-text-4xl);
  font-weight: 600;
}

@media (max-width: 900px) {
  .governance-console__split {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
