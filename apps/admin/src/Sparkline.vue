<script setup lang="ts">
import { computed } from 'vue'
const props = defineProps<{ values:(number|null)[]; unit?:string; ceiling?:number }>()
const hasData = computed(() => props.values.some(x => x !== null && Number.isFinite(x)))
const high = computed(() => props.ceiling ?? Math.max(1,...props.values.filter((x):x is number => x!==null && Number.isFinite(x))))
const segments = computed(() => {
 const out:string[]=[];let current:string[]=[]
 props.values.forEach((value,i)=>{if(value===null || !Number.isFinite(value)){if(current.length)out.push(current.join(' '));current=[];return}
 current.push(`${(i/Math.max(1,props.values.length-1)*600).toFixed(1)},${(118-Math.min(high.value,Math.max(0,value))/high.value*100).toFixed(1)}`)})
 if(current.length)out.push(current.join(' '));return out
})
</script>
<template><div class="spark-wrap"><div class="spark-scale">{{ high.toFixed(1) }} {{ unit }}</div><svg v-if="hasData" viewBox="0 0 600 140" preserveAspectRatio="none" role="img" aria-label="服务器真实采样趋势"><path d="M0 18H600 M0 68H600 M0 118H600" class="chart-grid"/><polyline v-for="(segment,i) in segments" :key="i" :points="segment" class="chart-line"/></svg><el-empty v-else description="尚无可用样本" :image-size="40"/><div class="spark-labels"><span>较早</span><span>最新 · 每 5 秒采样</span></div></div></template>
