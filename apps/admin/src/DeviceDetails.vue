<script setup lang="ts">
import {computed} from 'vue'
import {formatBytes,telemetryState} from './connection.mjs'
import Sparkline from './Sparkline.vue'
import type {Device,Sample} from './types'
const props=defineProps<{open:boolean;device:Device|null;healthy:boolean;serverTime:string|undefined;history:Sample[];loading:boolean}>()
const emit=defineEmits<{close:[];refresh:[]}>()
const report=computed(()=>props.device?.telemetry?.report)
const state=computed(()=>telemetryState(props.device,props.serverTime,props.healthy))
const label=computed(()=>({fresh:'设备报告有效',missing:'尚未上报',stale:'报告已过期',offline:'设备已离线',unknown:'状态未知'} as Record<string,string>)[state.value as string]??'状态未知')
const date=(v:string|number|undefined)=>v?new Date(typeof v==='number'?v*1000:v).toLocaleString('zh-CN',{hour12:false}):'—'
const duration=(s:number|undefined)=>s==null?'未上报':`${Math.floor(s/3600)} 小时 ${Math.floor(s%3600/60)} 分钟`
const enabled=(v:boolean|undefined)=>v===undefined?'未上报':v?'已启用':'未启用'
const cpu=computed(()=>report.value?.sample.cpu_percent)
const memoryPercent=computed(()=>{const s=report.value?.sample;return s?.memory_total&&s.memory_used!=null?Math.min(100,s.memory_used/s.memory_total*100):0})
function exportReport(){if(!props.device)return;const blob=new Blob([JSON.stringify({device_id:props.device.device.id,fingerprint:props.device.fingerprint,telemetry:props.device.telemetry??null},null,2)],{type:'application/json'});const url=URL.createObjectURL(blob);const a=document.createElement('a');a.href=url;a.download=`RemoteDesk-${props.device.device.id}-status.json`;a.click();window.setTimeout(()=>URL.revokeObjectURL(url),1000)}
</script>
<template>
 <el-drawer :model-value="open" title="设备详细信息" size="min(860px, 96vw)" destroy-on-close @close="emit('close')">
  <template v-if="device">
   <div class="device-detail-header"><div><h2>{{device.meta.alias||device.device.name}}</h2><span class="muted">{{device.device.id}}</span></div><el-tag :type="state==='fresh'?'success':'warning'">{{label}}</el-tag></div>
   <div class="detail-actions"><el-button :loading="loading" :disabled="!healthy" @click="emit('refresh')">刷新设备报告</el-button><el-button @click="exportReport">导出状态 JSON</el-button></div>
   <el-alert title="报告只包含设备硬件和资源状态，不包含屏幕、剪贴板、文件内容或私钥。硬件名称由设备报告，不等于编码器已经可用。" type="info" :closable="false"/>
   <el-tabs>
    <el-tab-pane label="设备概况">
     <el-descriptions :column="2" border>
      <el-descriptions-item label="主机名">{{report?.system.hostname||'未上报'}}</el-descriptions-item>
      <el-descriptions-item label="Agent 版本">{{report?.agent_version||'未上报'}}</el-descriptions-item>
      <el-descriptions-item label="操作系统">{{report?.system.os_version||report?.system.os||'未上报'}}</el-descriptions-item>
      <el-descriptions-item label="系统架构">{{report?.system.arch||'未上报'}}</el-descriptions-item>
      <el-descriptions-item label="CPU">{{report?.system.cpu_model||'未上报'}}</el-descriptions-item>
      <el-descriptions-item label="逻辑处理器">{{report?.system.cpu_cores??'未上报'}}</el-descriptions-item>
      <el-descriptions-item label="设备进程运行时间">{{duration(report?.uptime_seconds)}}</el-descriptions-item>
      <el-descriptions-item label="首次注册">{{date(device.device.registered_at)}}</el-descriptions-item>
      <el-descriptions-item label="最后心跳">{{date(device.last_seen)}}</el-descriptions-item>
      <el-descriptions-item label="服务器接收报告时间">{{date(device.telemetry?.received_at)}}</el-descriptions-item>
      <el-descriptions-item label="分组">{{device.meta.group||'未分组'}}</el-descriptions-item>
      <el-descriptions-item label="硬件采集">{{report?.system.hardware_probe==='ok'?'已读取':report?.system.hardware_probe==='partial'?'部分可读':'未取得'}}</el-descriptions-item>
      <el-descriptions-item label="备注" :span="2">{{device.meta.notes||'—'}}</el-descriptions-item>
      <el-descriptions-item label="公钥指纹" :span="2"><code class="breakable">{{device.fingerprint||'—'}}</code></el-descriptions-item>
     </el-descriptions>
     <h3>显卡与驱动</h3><el-table :data="report?.system.gpus??[]" empty-text="未采集到显卡信息"><el-table-column prop="name" label="显卡名称"/><el-table-column prop="driver" label="驱动版本"/></el-table>
     <h3>显示配置（CIM 当前值）</h3><el-table :data="report?.system.displays??[]" empty-text="未采集到显示配置"><el-table-column prop="name" label="显示适配器"/><el-table-column label="当前分辨率"><template #default="{row}">{{row.width}} × {{row.height}}</template></el-table-column><el-table-column prop="refresh_hz" label="刷新率 Hz"/></el-table>
     <p class="muted small">该值来自 Windows 显示适配器，不是完整的多显示器枚举。远控实际编码器、帧率和延迟需要媒体会话测量。</p>
    </el-tab-pane>
    <el-tab-pane label="资源与趋势">
     <el-alert v-if="state!=='fresh'" :title="label+'：下面是最后收到的采样，不代表当前状态。'" type="warning" :closable="false" class="mb"/>
     <div class="detail-kpis"><el-card shadow="never"><span class="muted">CPU 使用率</span><h2>{{cpu==null?'未采集':cpu.toFixed(1)+'%'}}</h2></el-card><el-card shadow="never"><span class="muted">内存使用</span><h2>{{formatBytes(report?.sample.memory_used)}}</h2><el-progress :percentage="memoryPercent" :show-text="false"/></el-card></div>
     <el-descriptions :column="2" border class="mt"><el-descriptions-item label="物理内存总量">{{formatBytes(report?.sample.memory_total)}}</el-descriptions-item><el-descriptions-item label="Agent 进程 RSS">{{formatBytes(report?.sample.process_rss)}}</el-descriptions-item><el-descriptions-item label="状态所在卷已用">{{formatBytes(report?.sample.disk_used)}}</el-descriptions-item><el-descriptions-item label="状态所在卷容量">{{formatBytes(report?.sample.disk_total)}}</el-descriptions-item><el-descriptions-item label="采样时间">{{date(report?.sample.at)}}</el-descriptions-item><el-descriptions-item label="Go 协程数">{{report?.sample.goroutines??'未上报'}}</el-descriptions-item></el-descriptions>
     <h3>最近资源历史</h3><Sparkline :values="history.map(s=>s.cpu_percent)" :ceiling="100" unit="%"/>
     <p class="muted small">最近 40 个设备报告，正常约 10 分钟；此曲线在打开或刷新详情时读取。服务器重启清空，停止上报 1 小时后清理。Windows 网卡速率目前未采集。</p>
    </el-tab-pane>
    <el-tab-pane label="网络与可用功能">
     <el-descriptions :column="1" border><el-descriptions-item label="服务器观察到的来源 IP">{{device.telemetry?.observed_ip||'未上报'}}<small class="subline">来自 HTTPS 连接，经过反向代理时可能是代理地址。</small></el-descriptions-item><el-descriptions-item label="本机网卡 IP"><div class="detail-tags"><el-tag v-for="ip in report?.local_ips??[]" :key="ip" type="info">{{ip}}</el-tag></div></el-descriptions-item><el-descriptions-item label="签名 TCP 候选"><div v-for="a in device.candidates.tcp??[]" :key="a"><code>{{a}}</code></div></el-descriptions-item><el-descriptions-item label="签名 UDP 候选"><div v-for="a in device.candidates.udp??[]" :key="a"><code>{{a}}</code></div></el-descriptions-item><el-descriptions-item label="运行模式">{{report?.hosting.mode==='service'?'Windows 服务':report?.hosting.mode==='interactive'?'当前登录用户':'未上报'}}</el-descriptions-item><el-descriptions-item label="桌面接入配置">{{enabled(report?.hosting.desktop_enabled)}}</el-descriptions-item><el-descriptions-item label="文件共享配置">{{enabled(report?.hosting.file_sharing_enabled)}}</el-descriptions-item><el-descriptions-item label="媒体程序存在">{{report?.hosting.media_worker_present===undefined?'未上报':report.hosting.media_worker_present?'存在（不等于 GPU 可用）':'未找到'}}</el-descriptions-item><el-descriptions-item label="本机 SSH 回环端口">{{report?.hosting.ssh_listening===undefined?'未上报':report.hosting.ssh_listening?'TCP 端口可连接（未验证 SSH 登录）':'无法连接'}}</el-descriptions-item><el-descriptions-item label="NAT 类型 / 直连延迟">未测量，不通过候选地址推断</el-descriptions-item></el-descriptions>
     <p class="muted small">以上是最后收到的设备报告。功能已启用仍需要目标设备本地公钥授权，后台管理员不能通过本页面绕过授权。</p>
    </el-tab-pane>
   </el-tabs>
  </template>
 </el-drawer>
</template>
<style scoped>
.device-detail-header,.detail-actions{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:18px}.device-detail-header h2{margin:0 0 4px}.detail-actions{justify-content:flex-start}.detail-kpis{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.detail-tags{display:flex;flex-wrap:wrap;gap:6px}.breakable{word-break:break-all}h3{margin-top:24px}.small{line-height:1.7}
</style>
