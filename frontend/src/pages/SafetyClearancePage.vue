<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue';
import EntityPage from '../components/EntityPage.vue';
import ClearancePanel from '../components/common/ClearancePanel.vue';
import type { DomainRecord } from '../types/domain';
import { ENTITY_CONFIGS } from '../types/status';
import { useSafetyClearanceStore } from '../stores/safety-clearance';
import { useAuth } from '../hooks/useAuth';

const store = useSafetyClearanceStore();
const { session } = useAuth();
const roleRank: Record<string, number> = { viewer: 1, operator: 2, reviewer: 3, admin: 4 };

const confirmClearance = (item: DomainRecord) => store.confirmClearance('clearance', item);

const pendingRecords = computed(() =>
  [...store.items]
    .filter((item) => item.status === 'pending')
    .sort((left, right) => Number(Boolean(right.interlockInvalidReason)) - Number(Boolean(left.interlockInvalidReason))),
);

function basisState(item: DomainRecord): { tone: 'invalid' | 'ok' | 'frozen'; text: string } {
  if (item.status === 'cleared') return { tone: 'frozen', text: '已放行 · 历史依据固化' };
  if (item.status === 'expired') return { tone: 'frozen', text: '已过期 · 历史依据保留' };
  if (item.interlockInvalidReason) return { tone: 'invalid', text: '联锁失效' };
  if (item.interlockBasisValid) return { tone: 'ok', text: '依据有效 · 可继续双人确认' };
  return { tone: 'frozen', text: '依据评估中' };
}

function versionText(expected?: number, current?: number): string {
  if (current === undefined) return `v${expected ?? 0}`;
  if (current !== expected) return `v${expected ?? 0} → 当前 v${current}`;
  return `v${expected ?? 0}`;
}

function canAct(item: DomainRecord): boolean {
  if (item.status !== 'pending' || item.interlockInvalidReason) return false;
  const rank = roleRank[session.value?.role || ''] || 0;
  if (!item.submittedBy) return rank >= roleRank.operator;
  return rank >= roleRank.reviewer && item.submittedBy !== session.value?.username;
}

function actionLabel(item: DomainRecord): string {
  return item.submittedBy ? '复核并放行' : '提交安全确认';
}

function refresh() {
  return store.load('clearance');
}

let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  timer = setInterval(() => void refresh(), 15000);
});
onUnmounted(() => {
  if (timer) clearInterval(timer);
});
</script>

<template>
  <EntityPage
    :config="ENTITY_CONFIGS[3]" :store="store" hide-transitions
    :create-defaults="{ planCode: 'MP-003', windowCode: 'WW-002' }"
  >
    <template #insight>
      <ClearancePanel :records="store.items" mode="clearance" @confirm="confirmClearance"/>
      <section class="risk-board basis-table" aria-label="许可联锁依据">
        <header>
          <div>
            <span class="eyebrow">INTERLOCK BASIS</span>
            <strong>许可—方案—窗口 联锁依据</strong>
          </div>
          <small>提交与放行时后端重读方案与窗口；每 15 秒自动回读，<el-button link type="primary" @click="refresh">立即刷新</el-button></small>
        </header>
        <el-table :data="pendingRecords" size="small" v-loading="store.loading">
          <el-table-column prop="code" label="许可" width="110"/>
          <el-table-column label="系泊方案" min-width="190">
            <template #default="{ row }">
              <div><strong>{{ row.planCode || '-' }}</strong> <small>{{ row.planStatus || '未知' }}</small></div>
              <small :class="{ 'basis-invalid': row.currentPlanVersion !== undefined && row.currentPlanVersion !== row.planVersion }">
                依据 {{ versionText(row.planVersion, row.currentPlanVersion) }}
              </small>
            </template>
          </el-table-column>
          <el-table-column label="风浪窗口" min-width="190">
            <template #default="{ row }">
              <div><strong>{{ row.windowCode || '-' }}</strong> <small>{{ row.windowStatus || '未知' }}</small></div>
              <small :class="{ 'basis-invalid': row.currentWindowVersion !== undefined && row.currentWindowVersion !== row.windowVersion }">
                依据 {{ versionText(row.windowVersion, row.currentWindowVersion) }}
              </small>
            </template>
          </el-table-column>
          <el-table-column label="提交 / 复核" width="170">
            <template #default="{ row }">
              <small>提交：{{ row.submittedBy || '待提交' }}</small><br/>
              <small>复核：{{ row.confirmedBy || '待复核' }}</small>
            </template>
          </el-table-column>
          <el-table-column label="联锁状态" min-width="240">
            <template #default="{ row }">
              <el-alert
                v-if="row.interlockInvalidReason" :title="row.interlockInvalidReason" type="error" :closable="false" show-icon
              />
              <span v-else :class="`basis-${basisState(row).tone}`">{{ basisState(row).text }}</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="140">
            <template #default="{ row }">
              <el-button v-if="canAct(row)" type="primary" size="small" @click="confirmClearance(row)">{{ actionLabel(row) }}</el-button>
              <span v-else-if="row.interlockInvalidReason" class="muted">禁止放行</span>
              <span v-else-if="row.submittedBy" class="muted">等待其他复核员</span>
              <span v-else class="muted">无操作权限</span>
            </template>
          </el-table-column>
        </el-table>
      </section>
    </template>
  </EntityPage>
</template>
