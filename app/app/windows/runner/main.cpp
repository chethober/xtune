#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <windows.h>

#include "flutter_window.h"
#include "utils.h"

int APIENTRY wWinMain(_In_ HINSTANCE instance, _In_opt_ HINSTANCE prev,
                      _In_ wchar_t *command_line, _In_ int show_command) {
  // one xtune at a time: launched again (perhaps while hidden in the tray), bring
  // the running one's window back instead
  HANDLE single = ::CreateMutexW(nullptr, TRUE, L"Local\\com.chethober.xtune");
  if (single != nullptr && ::GetLastError() == ERROR_ALREADY_EXISTS) {
    HWND running = ::FindWindowW(L"FLUTTER_RUNNER_WIN32_WINDOW", L"xtune");
    if (running != nullptr) {
      ::ShowWindow(running, SW_SHOW);
      ::ShowWindow(running, SW_RESTORE);
      ::SetForegroundWindow(running);
    }
    ::CloseHandle(single);
    return EXIT_SUCCESS;
  }

  // Attach to console when present (e.g., 'flutter run') or create a
  // new console when running with a debugger.
  if (!::AttachConsole(ATTACH_PARENT_PROCESS) && ::IsDebuggerPresent()) {
    CreateAndAttachConsole();
  }

  // Initialize COM, so that it is available for use in the library and/or
  // plugins.
  ::CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);

  flutter::DartProject project(L"data");

  std::vector<std::string> command_line_arguments =
      GetCommandLineArguments();

  project.set_dart_entrypoint_arguments(std::move(command_line_arguments));

  FlutterWindow window(project);
  Win32Window::Point origin(10, 10);
  Win32Window::Size size(1100, 760);
  if (!window.Create(L"xtune", origin, size)) {
    return EXIT_FAILURE;
  }
  window.SetQuitOnClose(true);

  ::MSG msg;
  while (::GetMessage(&msg, nullptr, 0, 0)) {
    ::TranslateMessage(&msg);
    ::DispatchMessage(&msg);
  }

  ::CoUninitialize();
  if (single != nullptr) {
    ::ReleaseMutex(single);
    ::CloseHandle(single);
  }
  return EXIT_SUCCESS;
}
