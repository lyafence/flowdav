package com.flowdav.app

import androidx.lifecycle.ViewModel

class MainViewModel : ViewModel() {
    var isManualMode = false
    var isEncrypted = true
    var isConnecting = false
}
