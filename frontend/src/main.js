/**
 * main.js - 商机单核查工具 v4.1 (Wails 版)
 * 前端逻辑：Vue3 + Wails runtime
 */

// Wails 自动生成的绑定（首次 wails build 后生成）
// 如果绑定文件不存在，使用 window.go 全局对象
let goApi;
let wailsRuntime;
try {
    goApi = await import('../wailsjs/go/main/App.js');
    wailsRuntime = await import('../wailsjs/runtime/runtime.js');
} catch (e) {
    console.warn('[main.js] Wails 绑定文件未生成，使用全局对象');
    goApi = null;
    wailsRuntime = null;
}

// 统一调用封装
async function call(method, ...args) {
    if (goApi && goApi[method]) {
        return goApi[method](...args);
    }
    // 回退到 window.go
    if (window.go && window.go.main && window.go.main.App) {
        return window.go.main.App[method](...args);
    }
    throw new Error(`Wails 绑定未就绪: ${method}`);
}

// 事件监听封装
function onEvent(name, callback) {
    if (wailsRuntime && wailsRuntime.EventsOn) {
        wailsRuntime.EventsOn(name, callback);
    } else if (window.runtime && window.runtime.EventsOn) {
        window.runtime.EventsOn(name, callback);
    }
}

// ==================== Vue 应用 ====================

const { createApp, ref, computed, onMounted, onUnmounted, nextTick, reactive } = Vue;

const app = createApp({
    setup() {
        // ==================== 状态 ====================
        const isLoggedIn = ref(false);
        const menu = ref('check');
        const showLogs = ref(false);

        // 登录页
        const username = ref('');
        const password = ref('');
        const captchaCode = ref('');
        const captchaImg = ref('');
        const captchaUuid = ref('');
        const rememberMe = ref(false);
        const loginLoading = ref(false);
        const passwordInput = ref(null);
        const captchaInput = ref(null);

        const LS_KEY = 'shangji_check_remember';

        // 弹窗
        const confirmModal = reactive({ show: false, msg: '', onOk: () => {}, onCancel: () => {} });
        function showConfirm(msg) {
            return new Promise((resolve) => {
                confirmModal.msg = msg;
                confirmModal.onOk = () => { confirmModal.show = false; resolve(true); };
                confirmModal.onCancel = () => { confirmModal.show = false; resolve(false); };
                confirmModal.show = true;
            });
        }
        const errorModal = reactive({ show: false, msg: '' });
        function showError(msg) { errorModal.msg = msg; errorModal.show = true; }

        // 主页
        const filePath = ref('');
        const fileName = ref('');
        const startTime = ref('');
        const endTime = ref('');
        const extracting = ref(false);
        const filter = ref('all');

        // 进度
        const batchPage = ref(0);
        const batchTotalPages = ref(0);
        const batchTotal = ref(0);
        const batchText = ref('');

        // 结果
        const results = ref([]);
        const logs = ref([]);
        const logContainer = ref(null);

        // 导出弹窗
        const exportModal = reactive({ show: false, path: '' });

        // AI 配置弹窗
        const aiModal = reactive({ show: false, saving: false, testing: false, message: '', error: false });
        const aiEnabled = ref(false);
        const aiEndpoint = ref('');
        const aiModel = ref('');
        const aiApiKey = ref('');
        const aiTimeout = ref(30);
        const aiHasKey = ref(false);
        const aiLocked = ref(false);

        // 聊天弹窗
        const chatModal = reactive({ show: false, customer: '', messages: [] });

        // ==================== 计算属性 ====================
        const batchProgress = computed(() => {
            if (batchTotalPages.value === 0) return 0;
            return Math.round((batchPage.value / batchTotalPages.value) * 100);
        });

        const filteredResults = computed(() => {
            if (filter.value === 'all') return results.value;
            return results.value.filter(r => r.intervention === filter.value);
        });

        // ==================== 通用方法 ====================
        function addLog(msg) {
            const ts = new Date().toLocaleTimeString('zh-CN', { hour12: false });
            logs.value.push(`[${ts}] ${msg}`);
            if (logs.value.length > 300) logs.value = logs.value.slice(-300);
            nextTick(() => {
                if (logContainer.value) logContainer.value.scrollTop = logContainer.value.scrollHeight;
            });
        }

        // ==================== Wails 事件监听 ====================
        function setupEvents() {
            onEvent('log', (msg) => addLog(msg));
            onEvent('batch-progress', (page, totalPages, count, total) => {
                batchPage.value = page;
                batchTotalPages.value = totalPages;
                batchText.value = `正在查询第 ${page}/${totalPages} 批，已获取 ${count}/${total} 条`;
            });
            onEvent('item-update', (item) => {
                const idx = results.value.findIndex(r => r.id === item.id);
                if (idx >= 0) results.value[idx] = { ...results.value[idx], ...item };
                else results.value.push(item);
            });
            onEvent('extract-start', (total) => {
                extracting.value = true;
                batchTotal.value = 0;
                results.value = [];
                addLog(`开始提取，共 ${total} 条商机`);
            });
            onEvent('extract-complete', (matched, total) => {
                extracting.value = false;
                addLog(`提取完成: ${matched}/${total} 匹配`);
            });
            onEvent('error', (error) => {
                extracting.value = false;
                addLog(`错误: ${error}`);
                showError(`发生错误: ${error}`);
            });
            onEvent('aborted', () => {
                extracting.value = false;
                addLog('提取已中止，可重新点击开始提取');
            });
        }

        // ==================== 登录方法 ====================
        async function refreshCaptcha() {
            try {
                const result = await call('RefreshCaptcha');
                if (result.img) {
                    captchaImg.value = result.img;
                    captchaUuid.value = result.uuid;
                    captchaCode.value = '';
                } else {
                    addLog(`验证码获取失败: ${result.error || '未知错误'}`);
                }
            } catch (e) { addLog(`验证码请求异常: ${e.message}`); }
        }

        function focusPassword() { if (passwordInput.value) passwordInput.value.focus(); }
        function focusCaptcha() { if (captchaInput.value) captchaInput.value.focus(); }

        async function doLogin() {
            if (!username.value || !password.value) { showError('请填写用户名和密码'); return; }
            if (!captchaCode.value) { showError('请填写验证码'); return; }
            if (!captchaUuid.value) { showError('验证码未加载，请点击刷新'); return; }
            loginLoading.value = true;
            try {
                const result = await call('Login', username.value, password.value, captchaCode.value, captchaUuid.value);
                if (result.success) {
                    addLog('登录成功');
                    if (rememberMe.value) {
                        try { localStorage.setItem(LS_KEY, JSON.stringify({ username: username.value, rememberMe: true })); } catch (e) {}
                    } else { try { localStorage.removeItem(LS_KEY); } catch (e) {} }
                    isLoggedIn.value = true;
                } else {
                    showError(result.error || '登录失败');
                    addLog(`登录失败: ${result.error}`);
                    refreshCaptcha();
                }
            } catch (e) {
                showError(`请求异常: ${e.message}`);
                addLog(`登录异常: ${e.message}`);
            } finally { loginLoading.value = false; }
        }

        async function doLogout() {
            const ok = await showConfirm('确认退出登录？');
            if (!ok) return;
            try { await call('ClearLogin'); isLoggedIn.value = false; results.value = []; addLog('已退出登录'); refreshCaptcha(); }
            catch (e) { addLog(`退出失败: ${e.message}`); }
        }

        async function clearSavedLogin() {
            const ok = await showConfirm('确认清除已保存的登录信息？');
            if (!ok) return;
            try {
                try { localStorage.removeItem(LS_KEY); } catch (e) {}
                await call('ClearLogin');
                username.value = ''; password.value = ''; captchaCode.value = ''; rememberMe.value = false;
                addLog('已清除登录信息'); refreshCaptcha();
            } catch (e) { addLog(`清除登录失败: ${e.message}`); }
        }

        // ==================== 主页方法 ====================
        async function selectFile() {
            try {
                const result = await call('SelectFile');
                if (result.path) { filePath.value = result.path; fileName.value = result.filename; addLog(`已选择文件: ${result.filename}`); }
            } catch (e) { addLog(`文件选择失败: ${e.message}`); }
        }

        async function startExtract() {
            if (!fileName.value) { alert('请先选择 Excel 文件'); return; }
            if (!startTime.value || !endTime.value) { alert('请选择时间范围'); return; }
            if (new Date(startTime.value) > new Date(endTime.value)) { alert('开始时间不能晚于结束时间'); return; }
            const days = Math.ceil((new Date(endTime.value) - new Date(startTime.value)) / (1000 * 60 * 60 * 24));
            if (days > 31) { alert(`查询时间跨度不能超过31天（当前跨度：${days}天）`); return; }
            extracting.value = true; results.value = []; batchPage.value = 0; batchTotalPages.value = 0; batchText.value = '正在启动...';
            try {
                const result = await call('StartExtract', startTime.value, endTime.value);
                if (!result.success) { extracting.value = false; alert(result.error); }
            } catch (e) { extracting.value = false; addLog(`启动提取失败: ${e.message}`); }
        }

        async function abortExtract() {
            try {
                const result = await call('AbortExtract');
                if (result.success) {
                    extracting.value = false;
                    addLog('正在中止提取...');
                }
            } catch (e) { addLog(`中止失败: ${e.message}`); }
        }

        async function exportResults() {
            try {
                const result = await call('ExportResults');
                if (result.success) {
                    addLog(`导出成功: ${result.filename}`);
                    exportModal.path = result.path; exportModal.show = true;
                } else { showError(`导出失败: ${result.error}`); }
            } catch (e) { addLog(`导出异常: ${e.message}`); showError(`导出异常: ${e.message}`); }
        }

        async function openAISettings() {
            try {
                const cfg = await call('GetAIConfig');
                aiEnabled.value = !!cfg.enabled;
                aiEndpoint.value = cfg.endpoint || '';
                aiModel.value = cfg.model || '';
                aiTimeout.value = cfg.timeout_seconds || 30;
                aiHasKey.value = !!cfg.has_api_key;
                aiLocked.value = !!cfg.locked;
                aiApiKey.value = '';
                aiModal.message = '';
                aiModal.error = false;
                aiModal.show = true;
            } catch (e) { showError(`读取 AI 配置失败: ${e.message}`); }
        }

        async function saveAISettings() {
            aiModal.saving = true;
            try {
                const result = await call('SaveAIConfig', aiEnabled.value, aiEndpoint.value, aiModel.value, aiApiKey.value, Number(aiTimeout.value) || 30);
                if (!result.success) throw new Error(result.error || '保存失败');
                aiHasKey.value = !!(result.config && result.config.has_api_key);
                aiApiKey.value = '';
                aiModal.message = '已保存，后续分析将按配置调用 AI。';
                aiModal.error = false;
                addLog('AI 配置已更新');
            } catch (e) { aiModal.message = e.message; aiModal.error = true; }
            finally { aiModal.saving = false; }
        }

        async function testAISettings() {
            aiModal.testing = true;
            try {
                const result = await call('TestAIConfig');
                if (!result.success) throw new Error(result.error || '测试失败');
                aiModal.message = `AI 连接成功（置信度 ${Math.round((result.confidence || 0) * 100)}%）`;
                aiModal.error = false;
            } catch (e) { aiModal.message = e.message; aiModal.error = true; }
            finally { aiModal.testing = false; }
        }

        function showChat(item) {
            const chatText = item.chat_text || '';
            if (!chatText) { alert('暂无聊天记录'); return; }
            chatModal.customer = item.customer_name;
            chatModal.messages = [];
            for (const line of chatText.split('\n')) {
                if (line.startsWith('---') || !line.trim()) continue;
                let role = '客户', roleType = 'customer', text = line;
                if (line.startsWith('[专员]')) { role = '专员'; roleType = 'specialist'; text = line.substring(4).trim(); }
                else if (line.startsWith('[一线]')) { role = '一线'; roleType = 'staff'; text = line.substring(4).trim(); }
                else if (line.startsWith('[客服]')) { role = '客服'; roleType = 'staff'; text = line.substring(4).trim(); }
                else if (line.startsWith('[客户]')) { role = '客户'; roleType = 'customer'; text = line.substring(4).trim(); }
                chatModal.messages.push({ role, roleType, text });
            }
            chatModal.show = true;
        }

        function stClass(s) { return s === '成功' ? 'st-success' : s === '查询中' ? 'st-info' : (s === '未匹配' || s === '失败') ? 'st-error' : s === '聊天失败' ? 'st-warning' : ''; }
        function ivClass(v) {
            return v === '有效营销' ? 'tag-yes' :
                v === '无关键信息介入' ? 'tag-light' :
                v === '插话' ? 'tag-error' :
                (v === '未介入' || v === '无记录' || v === '不纳入有效介入') ? 'tag-no' :
                (v === '获取失败' || v === '处理失败' || v === '错误') ? 'tag-error' : 'tag-info';
        }
        function situationClass(v) {
            return v === '键入1条关键信息' ? 'tag-info' :
                v === '键入2条关键信息' ? 'tag-yes' :
                v === '键入3条及以上关键信息' ? 'tag-yes' :
                v === '无相关会话' ? 'tag-error' :
                v === '无关键信息介入' ? 'tag-light' : 'tag-info';
        }

        // ==================== 生命周期 ====================
        onMounted(async () => {
            setupEvents();
            const today = new Date();
            const monthAgo = new Date(today.getTime() - 30 * 24 * 60 * 60 * 1000);
            endTime.value = today.toISOString().split('T')[0];
            startTime.value = monthAgo.toISOString().split('T')[0];
            try {
                const saved = localStorage.getItem(LS_KEY);
                if (saved) { const d = JSON.parse(saved); if (d.username) { username.value = d.username; rememberMe.value = !!d.rememberMe; } }
            } catch (e) {}
            try {
                const config = await call('GetConfig');
                if (config.username && !username.value) username.value = config.username;
                if (config.is_logged_in) { isLoggedIn.value = true; addLog('已加载本地登录信息'); }
                else refreshCaptcha();
            } catch (e) { refreshCaptcha(); }
            addLog('应用已启动');
        });

        return {
            isLoggedIn, menu, showLogs, username, password, captchaCode, captchaImg, rememberMe,
            loginLoading, passwordInput, captchaInput, confirmModal, errorModal, exportModal, aiModal,
            aiEnabled, aiEndpoint, aiModel, aiApiKey, aiTimeout, aiHasKey, aiLocked,
            filePath, fileName, startTime, endTime, extracting, filter,
            batchPage, batchTotalPages, batchTotal, batchText, batchProgress,
            results, filteredResults, logs, logContainer, chatModal,
            refreshCaptcha, focusPassword, focusCaptcha, doLogin, doLogout, clearSavedLogin, showError,
            selectFile, startExtract, abortExtract, exportResults, openAISettings, saveAISettings, testAISettings,
            showChat, stClass, ivClass, situationClass
        };
    }
});

app.mount('#app');
