# 商机单核查工具 — Mac 安装说明

## 下载

从 GitHub Actions 构建产物下载 `.app` 文件：

https://github.com/huanghh5/smart-check/actions

点击最近的构建记录 → 底部 **Artifacts** → `shangji-check-20-wails-macos`

## 安装

1. 下载后解压得到 `shangji-check-20-wails.app`
2. 将 `.app` 拖入 `/Applications`（应用程序）文件夹
3. 首次打开需要做安全放行（见下方）

## 首次打开：绕过安全限制

Mac 默认会拦截未签名的应用，提示"无法验证开发者"或"已损坏"。按以下步骤放行：

### 方法一：右键打开（推荐）

1. 打开 **访达（Finder）** → 进入 `/Applications`
2. **右键**（或 Control+点击）`shangji-check-20-wails.app`
3. 选择 **打开**
4. 弹出对话框点击 **打开**

> 只需第一次这样操作，之后可以正常双击打开。

### 方法二：系统设置放行

如果右键打开仍然被拦截：

1. 双击打开 app → 被系统拦截 → 点 **取消**
2. 打开 **系统设置** → **隐私与安全性**
3. 往下滚动，找到被拦截的提示 → 点 **仍要打开**
4. 输入密码确认 → 再次打开 app

### 方法三：终端命令（万能方法）

如果以上都不行，打开终端执行：

```bash
sudo xattr -rd com.apple.quarantine /Applications/shangji-check-20-wails.app
```

输入密码后回车，再双击打开 app 即可。

## 配置文件

首次运行后，应用目录会自动生成 `config.json`，内容如下：

```json
{
  "base_url": "http://你的服务器地址:端口"
}
```

修改 `base_url` 为实际的商机核查服务地址。

## 常见问题

**Q: 打开后白屏？**
A: 检查 `config.json` 中的 `base_url` 是否正确，确保服务器可达。

**Q: 提示"应用已损坏，无法打开"？**
A: 使用方法三的终端命令清除隔离属性。

**Q: 更新版本后需要重新放行吗？**
A: 如果覆盖安装到同一位置，通常不需要。删除旧版再装新版可能需要重新放行一次。
