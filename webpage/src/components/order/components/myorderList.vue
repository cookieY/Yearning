<style lang="less">
  @import '../../../styles/common.less';
  @import 'table.less';

  .top {
    padding: 10px;
    background: rgba(0, 153, 229, .7);
    color: #fff;
    text-align: center;
    border-radius: 2px;
  }
</style>
<template>
  <div>
    <Row>
      <Card>
        <p slot="title" style="height: 45px">
          <Icon type="android-send"></Icon>
          工单{{ this.$route.query.workid }}详细信息
          <br>
          <Button type="text" v-if="this.$route.query.status === 1" @click.native="rollback()">查看回滚语句</Button>
          <Button type="text" v-else-if="this.$route.query.status === 0 || this.$route.query.status ===4"
                  @click.native="repairOrder()">重新提交
          </Button>
          <Button type="text" v-if="this.$route.query.status === 2" @click.native="delorder()">工单撤销</Button>
          <Button type="text" @click.native="$router.go(-1)">返回</Button>
        </p>
        <Row>
          <Col span="24">
            <Table border :columns="tabcolumns" :data="TableDataNew" class="tabletop" style="background: #5cadff"
                   size="large"></Table>
          </Col>
        </Row>
      </Card>
    </Row>
    <BackTop :height="100" :bottom="200">
      <div class="top">返回顶端</div>
    </BackTop>

    <Modal v-model="reloadsql" :ok-text="'提交工单'" width="800" @on-ok="comorder">
      <Row>
        <Card>
          <div class="step-header-con">
            <h3>Yearning SQL平台审核工单</h3>
          </div>
          <p class="step-content"></p>
          <Form class="step-form" :label-width="100">
            <FormItem label="用户名:">
              <p>{{formItem.username}}</p>
            </FormItem>
            <FormItem label="机房:">
              <p>{{formItem.computer_room}}</p>
            </FormItem>
            <FormItem label="连接名:">
              <p>{{formItem.connection_name}}</p>
            </FormItem>
            <FormItem label="数据库库名:">
              <p>{{formItem.basename}}</p>
            </FormItem>
            <FormItem label="定时执行:">
              <p>{{formItem.delay}}</p>
            </FormItem>
            <FormItem>
                <Input v-model="sql" type="textarea" :rows="8"></Input>
            </FormItem>
            <FormItem label="工单提交说明:">
              <Input v-model="formItem.text" placeholder="最多不超过20个字"></Input>
            </FormItem>
            <FormItem label="是否备份">
              <RadioGroup v-model="formItem.backup">
                <Radio label="1">是</Radio>
                <Radio label="0">否</Radio>
              </RadioGroup>
            </FormItem>
          </Form>
        </Card>
      </Row>
    </Modal>
  </div>
</template>

<script>
  import axios from 'axios'
  //
  export default {
    name: 'myorder-list',
    data () {
      return {
        columnsName: [
          {
            title: '回滚语句',
            key: 'sql'
          }
        ],
        tabcolumns: [
          {
            title: 'sql语句',
            key: 'sql'
          },
          {
            title: '状态',
            key: 'state',
            width: 250
          },
          {
            title: '错误信息',
            key: 'error',
            width: 400
          },
          {
            title: '影响行数',
            key: 'affectrow',
            width: 100
          },
          {
            title: '执行时间/秒',
            key: 'execute_time',
            width: 200
          }
        ],
        TableDataNew: [],
        sql: '',
        openswitch: false,
        single: false,
        reloadsql: false,
        formItem: {
          computer_room: '',
          connection_name: '',
          basename: '',
          username: '',
          bundle_id: null
        },
        sqltype: null,
        dmlorddl: null
      }
    },
    methods: {
      rollback () {
        this.sql = ''
        if (this.TableDataNew[1].state.length === 40) {
          this.openswitch = true
          let opid = this.TableDataNew.map(item => item.sequence)
          opid.splice(0, 1)
          axios.post(`${this.$config.url}/detail/`, {'opid': JSON.stringify(opid), 'id': this.$route.query.id})
            .then(res => {
              this.formItem = res.data.data
              this.formItem.backup = '0'
              for (let i of res.data.sql) {
                this.sql += i.sql + '\n'
              }
              this.sqltype = res.data.type
              this.reloadsql = true
            })
            .catch(() => {
              this.$config.err_notice(this, '无法获得相关回滚数据,请确认备份库配置正确及备份规则')
            })
        } else {
          this.$Message.error('此工单没有备份或语句执行失败!')
        }
      },
      repairOrder () {
        axios.put(`${this.$config.url}/detail`, {'id': this.$route.query.id})
          .then(res => {
            this.formItem = res.data.data
            this.sql = res.data.sql
            this.sqltype = res.data.type
            this.formItem.backup = '0'
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
        this.reloadsql = true
      },
      comorder () {
        let sql = this.sql.replace(/(;|；)$/gi, '').replace(/\s/g, ' ').replace(/；/g, ';').split(';')
        axios.post(`${this.$config.url}/sqlsyntax/`, {
          'data': JSON.stringify(this.formItem),
          'sql': JSON.stringify(sql),
          'real_name': sessionStorage.getItem('real_name'),
          'type': this.dmlorddl,
          'id': this.formItem.bundle_id
        })
          .then(() => {
            this.$config.notice('工单已提交成功')
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      delorder () {
        let _list = []
        _list.push({'status': this.$route.query.status, 'id': this.$route.query.id})
        axios.post(`${this.$config.url}/undoOrder`, {
          'id': JSON.stringify(_list)
        })
          .then(res => {
            this.$config.notice(res.data)
            this.$router.go(-1)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      }
    },
    mounted () {
      axios.get(`${this.$config.url}/detail?workid=${this.$route.query.workid}&status=${this.$route.query.status}&id=${this.$route.query.id}`)
        .then(res => {
          this.TableDataNew = res.data.data
          this.dmlorddl = res.data.type
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
    }
  }
</script>
