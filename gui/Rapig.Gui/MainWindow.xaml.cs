using System.ComponentModel;
using System.IO;
using System.Reflection;
using System.Security.Cryptography;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Threading;
using Microsoft.Win32;
using Rapig.Gui.Services;

namespace Rapig.Gui;

public partial class MainWindow : Window
{
    private readonly CoreController core;
    private readonly DispatcherTimer statusTimer = new() { Interval = TimeSpan.FromSeconds(2) };
    private readonly DispatcherTimer logTimer = new() { Interval = TimeSpan.FromSeconds(1) };
    private bool busy;
    private bool loadingForm;
    private bool closingConfirmed;

    public MainWindow()
    {
        InitializeComponent();
        core = new CoreController(GuiConfig.Load());

        if (!core.EnsureCore())
        {
            MessageBox.Show(this, "内置核心程序释放失败。请确认本程序以管理员身份运行，且 %ProgramData%\\Rapig 可写。",
                "Rapig", MessageBoxButton.OK, MessageBoxImage.Error);
        }

        if (Application.Current is App app && app.StartMinimized)
        {
            WindowState = WindowState.Minimized;
        }

        loadingForm = true;
        LoadConfigToForm();
        loadingForm = false;
        VersionText.Text = ReadVersion();
        RefreshDataDirLabels();

        statusTimer.Tick += async (_, _) => await UpdateStatusAsync();
        logTimer.Tick += (_, _) => RefreshLog();
        statusTimer.Start();
        logTimer.Start();

        Loaded += async (_, _) =>
        {
            await UpdateStatusAsync();
            await EnsureAdminAsync();
            RefreshBootstrapInfo();
        };
    }

    private void LoadConfigToForm()
    {
        PortBox.Text = core.Config.Port.ToString();
        ProfileBox.Text = core.Config.Profile;
        SelectLogLevel(core.Config.LogLevel);
        CookieSecureBox.IsChecked = core.Config.CookieSecure;
        AutoStartToggle.IsChecked = AutoStart.IsEnabled();
    }

    private void RefreshDataDirLabels()
    {
        DataDirText.Text = core.DataDir;
    }

    private void SelectLogLevel(string level)
    {
        for (var i = 0; i < LogLevelBox.Items.Count; i++)
        {
            if (LogLevelBox.Items[i] is ComboBoxItem item &&
                string.Equals(item.Content?.ToString(), level, StringComparison.OrdinalIgnoreCase))
            {
                LogLevelBox.SelectedIndex = i;
                return;
            }
        }
        LogLevelBox.SelectedIndex = 1;
    }

    private static string ReadVersion()
    {
        try
        {
            return typeof(MainWindow).Assembly
                       .GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion ?? "dev";
        }
        catch
        {
            return "dev";
        }
    }

    /// <summary>首次运行自动创建管理员（随机密码，弹窗展示一次并写入 bootstrap 文件）。</summary>
    private async Task EnsureAdminAsync()
    {
        try
        {
            var marker = BootstrapFilePath();
            if (File.Exists(marker))
            {
                return;
            }
            var password = RandomPassword(16);
            var (ok, message) = await core.InitAdminAsync(password);
            if (ok)
            {
                RefreshBootstrapInfo();
                MessageBox.Show(this,
                    "已自动创建管理员账号：\n\n    用户名：admin\n    密码：" + password +
                    "\n\n密码同时已写入 " + marker + "，登录后请尽快修改密码（改密后该文件自动删除）。",
                    "Rapig 首次启动", MessageBoxButton.OK, MessageBoxImage.Information);
            }
            else if (!string.IsNullOrWhiteSpace(message))
            {
                LogBox.AppendText(message + Environment.NewLine);
            }
        }
        catch (Exception ex)
        {
            LogBox.AppendText("初始化管理员失败：" + ex.Message + Environment.NewLine);
        }
    }

    private string BootstrapFilePath() => Path.Combine(core.DataDir, "bootstrap", "initial-admin.txt");

    private string? ReadBootstrapPassword()
    {
        try
        {
            var p = BootstrapFilePath();
            if (!File.Exists(p))
            {
                return null;
            }
            var text = File.ReadAllText(p).Trim();
            var lines = text.Split(new[] { (char)10, (char)13 }, StringSplitOptions.RemoveEmptyEntries);
            foreach (var raw in lines)
            {
                var line = raw.Trim();
                if (line.StartsWith("password:", StringComparison.OrdinalIgnoreCase))
                {
                    return line["password:".Length..].Trim();
                }
                if (line.StartsWith("密码:", StringComparison.Ordinal) ||
                    line.StartsWith("密码：", StringComparison.Ordinal))
                {
                    return line[3..].Trim();
                }
            }
            return text;
        }
        catch
        {
            return null;
        }
    }

    private void RefreshBootstrapInfo()
    {
        var pw = ReadBootstrapPassword();
        if (pw == null)
        {
            BootstrapPasswordText.Text = "密码已修改（bootstrap 文件已删除）";
            BootstrapPasswordText.Foreground = (Brush)FindResource("Muted");
            RevealPasswordButton.IsEnabled = false;
            CopyPasswordButton.IsEnabled = false;
            return;
        }
        var shown = pw.Length > 40 ? pw[..40] + "…" : pw;
        BootstrapPasswordText.Text = "已生成，点「显示」查看明文";
        BootstrapPasswordText.Foreground = (Brush)FindResource("Muted");
        _bootstrapPassword = pw;
        RevealPasswordButton.IsEnabled = true;
        CopyPasswordButton.IsEnabled = true;
    }

    private string? _bootstrapPassword;

    private void RevealPassword_Click(object sender, RoutedEventArgs e)
    {
        var pw = _bootstrapPassword ?? ReadBootstrapPassword();
        if (pw == null)
        {
            MessageBox.Show(this, "初始密码文件已不存在（可能已改密）。如忘记密码，请在高级页重置。",
                "初始密码", MessageBoxButton.OK, MessageBoxImage.Information);
            return;
        }
        MessageBox.Show(this, "管理员账号：admin\n初始密码：" + pw +
                              "\n\n登录后请立刻修改密码。", "初始密码",
            MessageBoxButton.OK, MessageBoxImage.Information);
    }

    private void CopyPassword_Click(object sender, RoutedEventArgs e)
    {
        var pw = _bootstrapPassword ?? ReadBootstrapPassword();
        if (pw == null)
        {
            MessageBox.Show(this, "初始密码文件已不存在（可能已改密）。", "初始密码",
                MessageBoxButton.OK, MessageBoxImage.Information);
            return;
        }
        try
        {
            Clipboard.SetText(pw);
            FooterText.Text = "初始密码已复制到剪贴板。";
        }
        catch
        {
            MessageBox.Show(this, "复制失败，请用「显示」手动查看。", "初始密码",
                MessageBoxButton.OK, MessageBoxImage.Warning);
        }
    }

    private static string RandomPassword(int length)
    {
        const string alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
        var bytes = RandomNumberGenerator.GetBytes(length);
        var chars = new char[length];
        for (var i = 0; i < length; i++)
        {
            chars[i] = alphabet[bytes[i] % alphabet.Length];
        }
        return new string(chars);
    }

    private async Task UpdateStatusAsync()
    {
        if (busy)
        {
            return;
        }
        try
        {
            var health = await core.ProbeAsync();
            switch (health)
            {
                case CoreHealth.Running:
                    SetPill("#16A34A", "运行中");
                    RunStateText.Text = "运行中 · 点「打开网盘」开始使用";
                    FooterText.Text = "服务运行中 · 地址 " + core.AppUrl;
                    break;
                case CoreHealth.Unhealthy:
                    SetPill("#D97706", "异常");
                    RunStateText.Text = "运行中但健康检查失败，可尝试「重启服务」或查看日志";
                    FooterText.Text = "进程存在但健康检查未通过，可尝试「重启服务」或查看日志。";
                    break;
                default:
                    SetPill("#64748B", "已停止");
                    RunStateText.Text = "已停止 · 点「启动服务」开始";
                    FooterText.Text = "点「启动服务」即可使用；数据目录 " + core.DataDir;
                    break;
            }
            AddrText.Text = core.AppUrl;
            var running = health == CoreHealth.Running;
            StartButton.Content = running ? "服务运行中" : "启动服务";
            StartButton.IsEnabled = !running;
            StartButton.Background = running
                ? new SolidColorBrush((Color)ColorConverter.ConvertFromString("#16A34A"))
                : new SolidColorBrush((Color)ColorConverter.ConvertFromString("#F97316"));
            StopButton.IsEnabled = health != CoreHealth.Stopped;
            RestartButton.IsEnabled = health != CoreHealth.Stopped;
            OpenBrowserButton.IsEnabled = running;
            OpenDataButton.IsEnabled = OpenDataFooterButton.IsEnabled = Directory.Exists(core.DataDir);
        }
        catch
        {
            // 状态探测失败不打扰用户。
        }
    }

    private void SetPill(string hex, string text)
    {
        var brush = new SolidColorBrush((Color)ColorConverter.ConvertFromString(hex));
        brush.Freeze();
        StatusPill.Background = brush;
        StatusDot.Fill = Brushes.White;
        StatusText.Text = text;
    }

    private void RefreshLog()
    {
        var text = core.SnapshotLog();
        if (LogBox.Text != text)
        {
            LogBox.Text = text;
            LogBox.ScrollToEnd();
        }
    }

    private async Task RunBusyAsync(Func<Task<(bool Ok, string Message)>> work)
    {
        if (busy)
        {
            return;
        }
        busy = true;
        StartButton.IsEnabled = StopButton.IsEnabled = RestartButton.IsEnabled = false;
        try
        {
            var (ok, msg) = await work();
            if (!ok && !string.IsNullOrWhiteSpace(msg))
            {
                ShowStartFailure(msg);
            }
        }
        catch (Exception ex)
        {
            ShowStartFailure(ex.Message);
        }
        finally
        {
            busy = false;
            await UpdateStatusAsync();
        }
    }

    private async void Start_Click(object sender, RoutedEventArgs e) => await RunBusyAsync(core.StartAsync);

    private async void Stop_Click(object sender, RoutedEventArgs e) => await RunBusyAsync(core.StopAsync);

    private async void Restart_Click(object sender, RoutedEventArgs e) => await RunBusyAsync(core.RestartAsync);

    private void OpenBrowser_Click(object sender, RoutedEventArgs e) => core.OpenBrowser();

    private async void FirstRun_Click(object sender, RoutedEventArgs e)
    {
        await EnsureAdminAsync();
        RefreshBootstrapInfo();
        string text;
        if (_bootstrapPassword != null)
        {
            text = "管理员账号：admin" + Environment.NewLine +
                   "初始密码：" + _bootstrapPassword + Environment.NewLine + Environment.NewLine +
                   "可点「复制密码」复制；登录后请尽快修改密码。";
        }
        else
        {
            text = "管理员 admin 已存在（初始密码文件不存在，通常表示已改过密码）。" +
                   Environment.NewLine + "如忘记密码，请在下方「重置管理员密码」。";
        }
        MessageBox.Show(this, text, "首次启动", MessageBoxButton.OK, MessageBoxImage.Information);
    }

    private async void SaveAndRestart_Click(object sender, RoutedEventArgs e)
    {
        if (!int.TryParse(PortBox.Text.Trim(), out var port) || port is < 1 or > 65535)
        {
            MessageBox.Show(this, "端口需为 1–65535 的整数。", "配置", MessageBoxButton.OK, MessageBoxImage.Warning);
            return;
        }
        core.Config.Port = port;
        core.Config.Profile = string.IsNullOrWhiteSpace(ProfileBox.Text) ? "low" : ProfileBox.Text.Trim();
        if (LogLevelBox.SelectedItem is ComboBoxItem levelItem)
        {
            core.Config.LogLevel = levelItem.Content?.ToString() ?? "info";
        }
        core.Config.CookieSecure = CookieSecureBox.IsChecked == true;
        try
        {
            core.Config.Save();
        }
        catch (Exception ex)
        {
            MessageBox.Show(this, "保存配置失败：" + ex.Message, "配置", MessageBoxButton.OK, MessageBoxImage.Warning);
            return;
        }
        MessageBox.Show(this, "配置已保存，服务将以新配置重启。", "配置", MessageBoxButton.OK,
            MessageBoxImage.Information);
        await RunBusyAsync(core.RestartAsync);
    }

    private async void ResetPassword_Click(object sender, RoutedEventArgs e)
    {
        var pw = ResetPwBox.Password;
        if (pw.Length < 8)
        {
            MessageBox.Show(this, "密码至少 8 个字符。", "重置密码", MessageBoxButton.OK, MessageBoxImage.Warning);
            return;
        }
        var (ok, msg) = await core.ResetPasswordAsync(pw);
        ResetPwBox.Clear();
        if (ok)
        {
            RefreshBootstrapInfo();
        }
        MessageBox.Show(this, ok ? "管理员密码已重置。" : "重置失败：" + msg, "重置密码",
            MessageBoxButton.OK, ok ? MessageBoxImage.Information : MessageBoxImage.Warning);
    }

    private async void Doctor_Click(object sender, RoutedEventArgs e)
    {
        DoctorBox.Text = "正在检查…";
        DoctorBox.Text = await core.RunDoctorAsync();
    }

    private async void Backup_Click(object sender, RoutedEventArgs e)
    {
        var (ok, msg) = await core.BackupAsync();
        MessageBox.Show(this, ok ? "备份完成：\n" + msg : "备份失败：" + msg, "备份",
            MessageBoxButton.OK, ok ? MessageBoxImage.Information : MessageBoxImage.Warning);
    }

    private void OpenData_Click(object sender, RoutedEventArgs e)
    {
        if (Directory.Exists(core.DataDir))
        {
            core.OpenDataFolder();
        }
    }

    private void BackupDir_Click(object sender, RoutedEventArgs e)
    {
        try
        {
            Directory.CreateDirectory(GuiConfig.BackupDir);
            System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo
            {
                FileName = GuiConfig.BackupDir,
                UseShellExecute = true,
            });
        }
        catch (Exception ex)
        {
            MessageBox.Show(this, "无法打开备份目录：" + ex.Message, "备份",
                MessageBoxButton.OK, MessageBoxImage.Warning);
        }
    }

    private async void ChangeData_Click(object sender, RoutedEventArgs e)
    {
        var dlg = new OpenFolderDialog
        {
            Title = "选择新的数据目录（请选空目录）",
            InitialDirectory = Directory.Exists(core.DataDir)
                ? core.DataDir
                : Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments),
        };
        if (dlg.ShowDialog(this) != true)
        {
            return;
        }
        var target = Path.GetFullPath(dlg.FolderName).TrimEnd('\\', '/');
        var current = Path.GetFullPath(core.DataDir).TrimEnd('\\', '/');
        if (string.Equals(target, current, StringComparison.OrdinalIgnoreCase))
        {
            return;
        }
        if (Directory.Exists(target) && Directory.EnumerateFileSystemEntries(target).Any())
        {
            MessageBox.Show(this, "请选择一个空目录（程序会复制现有数据过去，不会覆盖已有文件）。",
                "更改数据目录", MessageBoxButton.OK, MessageBoxImage.Warning);
            return;
        }
        var r = MessageBox.Show(this,
            "将把当前数据复制到：\n" + target + "\n\n并重启服务。原目录保留不删，确认继续？",
            "更改数据目录", MessageBoxButton.YesNo, MessageBoxImage.Question, MessageBoxResult.No);
        if (r != MessageBoxResult.Yes)
        {
            return;
        }

        var oldDir = core.DataDir;
        try
        {
            if (await core.ProbeAsync() != CoreHealth.Stopped)
            {
                await core.StopAsync();
            }
            Directory.CreateDirectory(target);
            CopyDirectory(oldDir, target);
            core.Config.DataDir = target;
            core.Config.Save();
        }
        catch (Exception ex)
        {
            core.Config.DataDir = oldDir;
            try { core.Config.Save(); } catch { /* 忽略。 */ }
            RefreshDataDirLabels();
            MessageBox.Show(this, "更改数据目录失败：" + ex.Message, "更改数据目录",
                MessageBoxButton.OK, MessageBoxImage.Warning);
            return;
        }

        RefreshDataDirLabels();
        RefreshBootstrapInfo();
        await RunBusyAsync(core.StartAsync);
    }

    private static void CopyDirectory(string source, string target)
    {
        if (!Directory.Exists(source))
        {
            return;
        }
        foreach (var dir in Directory.GetDirectories(source, "*", SearchOption.AllDirectories))
        {
            Directory.CreateDirectory(Path.Combine(target, Path.GetRelativePath(source, dir)));
        }
        foreach (var file in Directory.GetFiles(source, "*", SearchOption.AllDirectories))
        {
            var dest = Path.Combine(target, Path.GetRelativePath(source, file));
            Directory.CreateDirectory(Path.GetDirectoryName(dest)!);
            File.Copy(file, dest, overwrite: false);
        }
    }

    private void AutoStart_Toggled(object sender, RoutedEventArgs e)
    {
        if (loadingForm)
        {
            return;
        }
        var exe = Environment.ProcessPath;
        if (string.IsNullOrEmpty(exe))
        {
            return;
        }
        var want = AutoStartToggle.IsChecked == true;
        try
        {
            AutoStart.SetEnabled(want, exe);
            core.Config.AutoStart = want;
            core.Config.Save();
            FooterText.Text = want ? "已开启开机自启（登录 Windows 时最小化启动）。" : "已关闭开机自启。";
        }
        catch (Exception ex)
        {
            MessageBox.Show(this, "设置开机自启失败：" + ex.Message, "开机自启",
                MessageBoxButton.OK, MessageBoxImage.Warning);
            AutoStartToggle.IsChecked = AutoStart.IsEnabled();
        }
    }

    private void ClearLog_Click(object sender, RoutedEventArgs e)
    {
        core.ClearLog();
        RefreshLog();
    }

    private void Exit_Click(object sender, RoutedEventArgs e) => Close();

    private async void Window_Closing(object? sender, CancelEventArgs e)
    {
        if (closingConfirmed)
        {
            return;
        }
        if (await core.ProbeAsync() == CoreHealth.Stopped)
        {
            closingConfirmed = true;
            return;
        }
        var r = MessageBox.Show(this, "关闭窗口将停止网盘服务，确定吗？", "退出 Rapig",
            MessageBoxButton.YesNo, MessageBoxImage.Question, MessageBoxResult.No);
        if (r != MessageBoxResult.Yes)
        {
            e.Cancel = true;
            return;
        }
        e.Cancel = true;
        closingConfirmed = true;
        await core.StopAsync();
        Close();
    }

    private void ShowStartFailure(string msg)
    {
        MessageBox.Show(this, msg, "启动失败", MessageBoxButton.OK, MessageBoxImage.Warning);
    }
}
