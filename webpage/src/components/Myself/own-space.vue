<style lang="less">
  @import './own-space.less';
</style>

<template>
  <div>
    <Card>
      <p slot="title">
        <Icon type="person"></Icon>
        个人信息
      </p>
      <div>
        <Form ref="userForm" :model="userForm" :label-width="100" label-position="right">
          <FormItem label="用户名：" prop="name">
            <div style="display:inline-block;width:300px;">
              <span>{{ userForm.username }}</span>
            </div>
          </FormItem>
          <FormItem label="部门：">
            <span>{{ userForm.department }}</span>
          </FormItem>
          <FormItem label="权限分类：">
            <span>{{ userForm.group }}</span>
          </FormItem>
          <FormItem label="邮箱：">
            <span>{{ userForm.email }}</span>
          </FormItem>
          <FormItem label="具体权限：">
            <br>
            <FormItem label="DDL是否可见:">
              <p>{{formItem.ddl}}</p>
            </FormItem>
            <FormItem label="可访问的连接名:" v-if="formItem.ddl === '是'">
              <p>{{formItem.ddlcon}}</p>
            </FormItem>
            <FormItem label="DML是否可见:">
              <p>{{formItem.dml}}</p>
            </FormItem>
            <FormItem label="可访问的连接名:" v-if="formItem.dml === '是'">
              <p>{{formItem.dmlcon}}</p>
            </FormItem>
            <FormItem label="查询是否可见:">
              <p>{{formItem.query}}</p>
            </FormItem>
            <FormItem label="可访问的连接名:" v-if="formItem.query === '是'">
              <p>{{formItem.querycon}}</p>
            </FormItem>
            <FormItem label="字典是否可见:">
              <p>{{formItem.dic}}</p>
            </FormItem>
            <FormItem label="可访问的连接名:" v-if="formItem.dic === '是'">
              <p>{{formItem.diccon}}</p>
            </FormItem>
            <FormItem label="用户管理权限:">
              <p>{{formItem.user}}</p>
            </FormItem>
            <FormItem label="数据库管理权限:">
              <p>{{formItem.base}}</p>
            </FormItem>
          </FormItem>
          <FormItem label="编辑：">
            <Button type="warning" size="small" @click="editPasswordModal=true">修改密码</Button>
            <Button type="primary" size="small" @click="editEmailModal=true">修改邮箱</Button>
            <Button type="success" size="small" @click="ApplyForPermission">权限申请</Button>
          </FormItem>
        </Form>
      </div>
    </Card>
    <Modal v-model="editPasswordModal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">修改密码</h3>
      <Form ref="editPasswordForm" :model="editPasswordForm" :label-width="100" label-position="right"
            :rules="passwordValidate">
        <FormItem label="原密码" prop="oldPass" :error="oldPassError">
          <Input v-model="editPasswordForm.oldPass" placeholder="请输入现在使用的密码" type="password"></Input>
        </FormItem>
        <FormItem label="新密码" prop="newPass">
          <Input v-model="editPasswordForm.newPass" placeholder="请输入新密码，至少6位字符" type="password"></Input>
        </FormItem>
        <FormItem label="确认新密码" prop="rePass">
          <Input v-model="editPasswordForm.rePass" placeholder="请再次输入新密码" type="password"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editPasswordModal=false">取消</Button>
        <Button type="primary" :loading="savePassLoading" @click="saveEditPass">保存</Button>
      </div>
    </Modal>
    <Modal v-model="editEmailModal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">邮箱修改</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="邮箱地址">
          <Input v-model="editEmailForm.mail"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editEmailModal=false">取消</Button>
        <Button type="primary" :loading="savePassLoading" @click="saveEmail">保存</Button>
      </div>
    </Modal>

    <Modal v-model="editInfodModal" :width="1000">
      <h3 slot="header" style="color:#2D8CF0">权限申请单</h3>
      <Form :label-width="120" label-position="right">
        <template>
          <FormItem label="DDL及索引权限:">
            <RadioGroup v-model="permission.ddl">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.ddl === '1'">
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.ddl"
                  :value="checkAll.ddl"
                  @click.prevent.native="ddlCheckAll('ddlcon', 'ddl', 'connection')">全选
                </Checkbox>
              </div>
              <CheckboxGroup v-model="permission.ddlcon">
                <Checkbox v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">
                  {{i.connection_name}}
                </Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="DML权限:">
            <RadioGroup v-model="permission.dml">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.dml === '1'">
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.dml"
                  :value="checkAll.dml"
                  @click.prevent.native="ddlCheckAll('dmlcon', 'dml', 'connection')">全选
                </Checkbox>
              </div>
              <CheckboxGroup v-model="permission.dmlcon">
                <Checkbox v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">
                  {{i.connection_name}}
                </Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
          <br>
          <FormItem label="数据查询权限:">
            <RadioGroup v-model="permission.query">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.query === '1'">
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.query"
                  :value="checkAll.query"
                  @click.prevent.native="ddlCheckAll('querycon', 'query', 'connection')">全选</Checkbox>
              </div>
              <CheckboxGroup v-model="permission.querycon">
                <Checkbox  v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">{{i.connection_name}}</Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="选择上级审核人:">
            <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
              <Checkbox
                :indeterminate="indeterminate.person"
                :value="checkAll.person"
                @click.prevent.native="ddlCheckAll('person', 'person', 'person')">全选
              </Checkbox>
            </div>
            <CheckboxGroup v-model="permission.person">
              <Checkbox v-for="i in connectionList.person" :label="i.username" :key="i.username">{{i.username}}
              </Checkbox>
            </CheckboxGroup>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="数据字典权限:">
            <RadioGroup v-model="permission.dic">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.dic === '1'">
            <FormItem label="数据字典修改权限:">
              <RadioGroup v-model="permission.dicedit">
                <Radio label="1">是</Radio>
                <Radio label="0">否</Radio>
              </RadioGroup>
            </FormItem>
            <FormItem label="数据字典导出权限:">
              <RadioGroup v-model="permission.dicexport">
                <Radio label="1">是</Radio>
                <Radio label="0">否</Radio>
              </RadioGroup>
            </FormItem>
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.dic"
                  :value="checkAll.dic"
                  @click.prevent.native="ddlCheckAll('diccon', 'dic', 'dic')">全选
                </Checkbox>
              </div>
              <CheckboxGroup v-model="permission.diccon">
                <Checkbox v-for="i in connectionList.dic" :label="i.Name" :key="i.Name">{{i.Name}}</Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
        </template>
        <template v-if="this.userForm.group === 'admin'">
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="用户管理权限:">
            <RadioGroup v-model="permission.user">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="数据库管理权限:">
            <RadioGroup v-model="permission.base">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
        </template>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editInfodModal=false">取消</Button>
        <Button type="primary" @click="PutPermissionData">保存</Button>
      </div>
    </Modal>
  </div>
</template>

<script>
  //
  import util from '../../libs/util'
  import axios from 'axios'

  const exchangetype = function typeok (vl) {
    if (typeof vl === 'string') {
      if (vl === '1') {
        return '是'
      } else {
        return '否'
      }
    } else {
      return vl.toString()
    }
  }
  export default {
    name: 'own-space',
    data () {
      const valideRePassword = (rule, value, callback) => {
        if (value !== this.editPasswordForm.newPass) {
          callback(new Error('两次输入密码不一致'))
        } else {
          callback()
        }
      }
      return {
        editEmailModal: false,
        editEmailForm: {
          mail: ''
        },
        userForm: {},
        formItem: {
          ddl: '',
          ddlcon: ''
        },
        uid: '', // 登录用户的userId
        save_loading: false,
        editPasswordModal: false, // 修改密码模态框显示
        savePassLoading: false,
        oldPassError: '',
        checkIdentifyCodeLoading: false,
        editPasswordForm: {
          oldPass: '',
          newPass: '',
          rePass: ''
        },
        passwordValidate: {
          oldPass: [{
            required: true,
            message: '请输入原密码',
            trigger: 'blur'
          }],
          newPass: [{
            required: true,
            message: '请输入新密码',
            trigger: 'blur'
          },
            {
              min: 6,
              message: '请至少输入6个字符',
              trigger: 'blur'
            },
            {
              max: 32,
              message: '最多输入32个字符',
              trigger: 'blur'
            }
          ],
          rePass: [{
            required: true,
            message: '请再次输入新密码',
            trigger: 'blur'
          },
            {
              validator: valideRePassword,
              trigger: 'blur'
            }
          ]
        },
        editInfodModal: false,
        permission: {
          ddl: '0',
          ddlcon: [],
          dml: '0',
          dmlcon: [],
          dic: '0',
          diccon: [],
          dicedit: '0',
          dicexport: '0',
          index: '0',
          indexcon: [],
          user: '0',
          base: '0'
        },
        indeterminate: {
          ddl: true,
          dml: true,
          query: true,
          dic: true,
          person: true
        },
        checkAll: {
          ddl: false,
          dml: false,
          query: false,
          dic: false,
          person: false
        },
        connectionList: {
          connection: [],
          dic: [],
          person: []
        }
      }
    },
    methods: {
      saveEditPass () {
        this.$refs['editPasswordForm'].validate((valid) => {
          if (valid) {
            this.savePassLoading = true
            axios.post(`${util.url}/otheruser/changepwd`, {
              'username': sessionStorage.getItem('user'),
              'new': this.editPasswordForm.newPass,
              'old': this.editPasswordForm.oldPass
            })
              .then(res => {
                util.notice(res.data)
                this.editPasswordModal = false
              })
              .catch(error => {
                util.err_notice(error)
              })
            this.savePassLoading = false
          }
        })
        for (let i in this.editPasswordForm) {
          this.editPasswordForm[i] = ''
        }
      },
      saveEmail () {
        this.savePassLoading = true
        axios.put(`${util.url}/otheruser/mail`, {'mail': this.editEmailForm.mail})
          .then(res => {
            util.notice(res.data)
            this.editEmailModal = false
          })
          .catch(error => {
            util.err_notice(error)
          })
        this.savePassLoading = false
      },
      init () {
        axios.put(`${util.url}/homedata/ownspace`, {
          'user': sessionStorage.getItem('user')
        })
          .then(res => {
            this.userForm = res.data.userinfo
            this.formItem.ddl = exchangetype(res.data.permissons.ddl)
            this.formItem.ddlcon = exchangetype(res.data.permissons.ddlcon)
            this.formItem.dml = exchangetype(res.data.permissons.dml)
            this.formItem.dmlcon = exchangetype(res.data.permissons.dmlcon)
            this.formItem.dic = exchangetype(res.data.permissons.dic)
            this.formItem.diccon = exchangetype(res.data.permissons.diccon)
            this.formItem.query = exchangetype(res.data.permissons.query)
            this.formItem.querycon = exchangetype(res.data.permissons.querycon)
            this.formItem.user = exchangetype(res.data.permissons.user)
            this.formItem.base = exchangetype(res.data.permissons.base)
          })
      },
      ApplyForPermission () {
        this.editInfodModal = true
        axios.get(`${util.url}/userinfo/permissions?user=${sessionStorage.getItem('user')}`)
          .then(res => {
            this.permission = res.data
          })
      },
      ddlCheckAll (name, indeterminate, ty) {
        if (this.indeterminate[indeterminate]) {
          this.checkAll[indeterminate] = false
        } else {
          this.checkAll[indeterminate] = !this.checkAll[indeterminate]
        }
        this.indeterminate[indeterminate] = false
        if (this.checkAll[indeterminate]) {
          if (ty === 'dic') {
            this.permission[name] = this.connectionList[ty].map(vl => vl.Name)
          } else if (ty === 'person') {
            this.permission[name] = this.connectionList[ty].map(vl => vl.username)
          } else {
            this.permission[name] = this.connectionList[ty].map(vl => vl.connection_name)
          }
        } else {
          this.permission[name] = []
        }
      },
      PutPermissionData () {
        axios.post(`${util.url}/apply_grained/`, {'grained_list': JSON.stringify(this.permission)})
          .then(res => {
            util.notice(res.data)
            this.editInfodModal = false
          })
          .catch(error => {
            util.err_notice(error)
          })
      }
    },
    mounted () {
      this.init()
      axios.put(`${util.url}/workorder/connection`, {'permissions_type': 'own_space'})
        .then(res => {
          this.connectionList.connection = res.data['connection']
          this.connectionList.dic = res.data['dic']
          this.connectionList.person = res.data['person']
        })
        .catch(error => {
          util.err_notice(error)
        })
    }
  }
</script>
