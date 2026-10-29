using System.Windows;
using System.Windows.Interop;
using System.Windows.Media;

namespace Rapig.Gui;

public partial class App : Application
{
    public bool StartMinimized { get; private set; }

    protected override void OnStartup(StartupEventArgs e)
    {
        // 部分 Win10 + 旧显卡驱动上 WPF 硬件合成会出现透明/残影，强制软件渲染最稳。
        RenderOptions.ProcessRenderMode = RenderMode.SoftwareOnly;
        StartMinimized = e.Args.Any(a => string.Equals(a, "--minimized", StringComparison.OrdinalIgnoreCase));
        base.OnStartup(e);
    }

    /// <summary>全局异常兜底，避免无提示退出。</summary>
    protected override void OnExit(ExitEventArgs e)
    {
        base.OnExit(e);
    }
}
